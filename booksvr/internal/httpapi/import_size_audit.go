package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcefile"
	"time"
)

const importSizeCandidateQuery = `SELECT f.id,f.book_id,f.path,f.format FROM book_formats f NOT INDEXED LEFT JOIN source_sync_formats s ON s.format_id=f.id WHERE f.id>? AND f.id<=? AND (f.size<? OR (s.missing=0 AND s.size<? AND f.path NOT LIKE 'talebook-zip:v1:%')) ORDER BY f.id LIMIT 1`

func importSizeAuditStatus(db *sql.DB) map[string]any {
	var generation, state, message string
	var minimum, total, checked, removed, books, unreadable, updated int64
	e := db.QueryRow(`SELECT generation,minimum,total,checked,removed_formats,removed_books,unreadable,state,error,updated_at FROM import_size_audit WHERE id=1`).Scan(&generation, &minimum, &total, &checked, &removed, &books, &unreadable, &state, &message, &updated)
	if e == sql.ErrNoRows {
		return map[string]any{"state": "idle"}
	}
	if e != nil {
		return map[string]any{"state": "error", "error": e.Error()}
	}
	immediate := state == "urgent"
	if immediate {
		state = "running"
	}
	return map[string]any{"immediate_requested": immediate, "state": state, "minimum_kib": minimum / 1024, "total": total, "checked": checked, "removed_formats": removed, "removed_books": books, "unreadable": unreadable, "error": message, "updated_at": updated}
}
func attachImportSizeAudit(parent context.Context, opts ServeOptions, books, users *sql.DB) (ServeOptions, func(), error) {
	if e := library.EnsureImportSizeAudit(books); e != nil {
		return opts, nil, e
	}
	var auditDB *sql.DB
	if opts.DBPath != "" {
		var e error
		auditDB, e = openExistingDB(opts.DBPath, 100)
		if e != nil {
			return opts, nil, e
		}
		auditDB.SetMaxOpenConns(1)
		auditDB.SetMaxIdleConns(1)
		books = auditDB
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	var stepMu, cacheMu sync.Mutex
	var cached map[string]any
	var expires time.Time
	var revision uint64
	readSettings := func() map[string]any {
		cacheMu.Lock()
		defer cacheMu.Unlock()
		r := importSettingsRevision.Load()
		if cached == nil || time.Now().After(expires) || revision != r {
			cached = LoadSettings(opts.SettingsPath)
			expires = time.Now().Add(time.Second)
			revision = r
		}
		return cached
	}
	var waiting atomic.Value
	waiting.Store("")
	previous := opts.ImportWatchStatus
	opts.ImportWatchStatus = func() map[string]any {
		out := map[string]any{}
		if previous != nil {
			out = previous()
		}
		status := importSizeAuditStatus(books)
		if status["state"] == "running" {
			status["waiting_reason"] = waiting.Load().(string)
		}
		out["size_audit"] = status
		return out
	}
	step := func(c context.Context, initialize bool) error {
		settings := readSettings()
		request := settingString(settings, "import_size_audit_requested")
		if request == "" {
			return nil
		}
		current := opts
		current.ImportDir = importDirFromSettings(opts.ImportDir, settings)
		current.SettingsPath = ""
		return runImportSizeAuditStep(c, current, books, users, request, importMinSize(settings)*1024, initialize)
	}
	failure := func(e error) {
		if e != nil {
			waiting.Store("retry")
			books.Exec(`UPDATE import_size_audit SET error=?,updated_at=? WHERE id=1`, e.Error(), time.Now().Unix())
		}
	}
	// The importer already owns its write mutex here, with its current transaction closed.
	if opts.ImportMaintenance != nil {
		opts.ImportMaintenance.Set(func(c context.Context) {
			if ctx.Err() != nil || c.Err() != nil || !stepMu.TryLock() {
				return
			}
			defer stepMu.Unlock()
			limit := 1
			var state string
			books.QueryRow(`SELECT state FROM import_size_audit WHERE id=1`).Scan(&state)
			if state != "running" && state != "urgent" {
				return
			}
			if state == "urgent" {
				limit = 4
			}
			until := time.Now().Add(100 * time.Millisecond)
			waiting.Store("")
			for i := 0; i < limit && ctx.Err() == nil && c.Err() == nil; i++ {
				if e := step(ctx, false); e != nil {
					failure(e)
					break
				}
				if time.Now().After(until) {
					break
				}
			}
		})
	}
	opts.RequestImportSizeAudit = func() error {
		stepMu.Lock()
		defer stepMu.Unlock()
		settings := readSettings()
		minimum := importMinSize(settings) * 1024
		if minimum <= 0 {
			return fmt.Errorf("请先在导入设置中将最小文件大小设为大于0")
		}
		if settingString(settings, "import_size_audit_requested") != "" {
			if e := step(ctx, true); e != nil {
				return e
			}
		}
		var state string
		var oldMinimum int64
		e := books.QueryRow(`SELECT state,minimum FROM import_size_audit WHERE id=1`).Scan(&state, &oldMinimum)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if (state != "running" && state != "urgent") || minimum != oldMinimum {
			if e = SaveSettings(opts.SettingsPath, map[string]any{"import_size_audit_requested": fmt.Sprint(time.Now().UnixNano())}); e != nil {
				return e
			}
			if e = step(ctx, true); e != nil {
				return e
			}
		}
		if _, e = books.Exec(`UPDATE import_size_audit SET state='urgent',error='' WHERE id=1`); e != nil {
			return e
		}
		waiting.Store("import_batch")
		select {
		case wake <- struct{}{}:
		default:
		}
		return nil
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		next := time.Time{}
		initialized := ""
		var lastMinimum int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				next = time.Time{}
			case <-ticker.C:
			}
			if time.Now().Before(next) {
				continue
			}
			settings := readSettings()
			request := settingString(settings, "import_size_audit_requested")
			minimum := importMinSize(settings) * 1024
			if request == "" {
				next = time.Now().Add(time.Second)
				continue
			}
			if initialized != request || lastMinimum != minimum {
				stepMu.Lock()
				e := step(ctx, true)
				stepMu.Unlock()
				if e != nil {
					failure(e)
					next = time.Now().Add(time.Second)
					continue
				}
				initialized = request
				lastMinimum = minimum
			}
			work := func() error { stepMu.Lock(); defer stepMu.Unlock(); waiting.Store(""); return step(ctx, false) }
			ran := true
			var e error
			if opts.TryImportMaintenance != nil {
				ran, e = opts.TryImportMaintenance(work)
			} else {
				e = work()
			}
			if !ran {
				waiting.Store("import_batch")
				next = time.Now().Add(time.Second)
			}
			if e != nil {
				failure(e)
				next = time.Now().Add(time.Second)
			}
			if importSizeAuditStatus(books)["state"] != "running" {
				next = time.Now().Add(time.Second)
			}
		}
	}()
	return opts, func() {
		if opts.ImportMaintenance != nil {
			opts.ImportMaintenance.Set(nil)
		}
		cancel()
		<-done
		if auditDB != nil {
			auditDB.Close()
		}
	}, nil
}
func runImportSizeAuditStep(ctx context.Context, opts ServeOptions, db, users *sql.DB, requested string, minimum int64, initializeOnly ...bool) error {
	if len(initializeOnly) == 0 || !initializeOnly[0] {
		if e := applyImportSizeAuditRemoval(db, users); e != nil {
			return e
		}
	}
	var generation, state string
	var cursor, maxID, savedMinimum int64
	e := db.QueryRow(`SELECT generation,cursor,max_id,state,minimum FROM import_size_audit WHERE id=1`).Scan(&generation, &cursor, &maxID, &state, &savedMinimum)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if requested != generation {
		var total int64
		if e = db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM book_formats`).Scan(&maxID); e != nil {
			return e
		}
		if e = db.QueryRow(`SELECT count(*) FROM (SELECT id FROM book_formats WHERE size<? AND id<=? UNION SELECT f.id FROM source_sync_formats s JOIN book_formats f ON f.id=s.format_id WHERE s.missing=0 AND s.size<? AND f.id<=? AND f.path NOT LIKE 'talebook-zip:v1:%')`, minimum, maxID, minimum, maxID).Scan(&total); e != nil {
			return e
		}
		state = "running"
		if minimum == 0 {
			state = "disabled"
		}
		_, e = db.Exec(`INSERT INTO import_size_audit(id,generation,minimum,max_id,total,state,updated_at) VALUES(1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,minimum=excluded.minimum,cursor=0,max_id=excluded.max_id,total=excluded.total,checked=0,removed_formats=0,removed_books=0,unreadable=0,state=excluded.state,error='',updated_at=excluded.updated_at`, requested, minimum, maxID, total, state, time.Now().Unix())
		return e
	}
	if savedMinimum != minimum {
		next := "cancelled"
		if minimum == 0 {
			next = "disabled"
		}
		_, e = db.Exec(`UPDATE import_size_audit SET state=?,updated_at=? WHERE id=1 AND generation=?`, next, time.Now().Unix(), requested)
		return e
	}
	if len(initializeOnly) > 0 && initializeOnly[0] {
		return nil
	}
	if state != "running" && state != "urgent" {
		return nil
	}
	var id, bookID int64
	var path, format string
	e = db.QueryRow(importSizeCandidateQuery, cursor, maxID, minimum, minimum).Scan(&id, &bookID, &path, &format)
	if e == sql.ErrNoRows {
		_, e = db.Exec(`UPDATE import_size_audit SET state='done',updated_at=? WHERE id=1 AND generation=?`, time.Now().Unix(), requested)
		return e
	}
	if e != nil {
		return e
	}
	// Check mount health before touching source bytes. A protected root is never a deletion signal.
	rootCfg := LiveImportDir(opts.ImportDir, opts.SettingsPath)
	roots := importdir.ConfiguredAllowedRoots(rootCfg)
	health, e := library.CheckSourceRoots(ctx, db, roots)
	if e != nil {
		return e
	}
	source := path
	readable := true
	if sourcefile.IsReference(path) {
		ref, e := sourcefile.Decode(path)
		if e != nil {
			readable = false
		} else {
			source = ref.Archive
		}
	}
	for root, h := range health {
		if importdir.IsWithin(source, root) && h != "ok" {
			readable = false
		}
	}
	physical := path
	release := func() {}
	var before os.FileInfo
	if readable {
		if !sourcefile.IsReference(physical) && !filepath.IsAbs(physical) {
			physical = filepath.Join(opts.LibraryRoot, physical)
		}
		physical, release, e = sourcefile.Acquire(ctx, physical)
		if e != nil {
			readable = false
		} else {
			before, e = os.Stat(physical)
			readable = e == nil && before.Mode().IsRegular()
		}
	}
	defer release()
	removed, e := library.PruneImportFormat(ctx, db, requested, id, path, physical, before, minimum, !readable)
	if e != nil {
		return e
	}
	_ = removed
	return applyImportSizeAuditRemoval(db, users)
}
func applyImportSizeAuditRemoval(db, users *sql.DB) error {
	if users == nil {
		return nil
	}
	var generation, path, format string
	var id, bookID, minimum int64
	e := db.QueryRow(`SELECT generation,format_id,book_id,path,format,minimum FROM import_size_audit_removed WHERE applied=0 ORDER BY rowid LIMIT 1`).Scan(&generation, &id, &bookID, &path, &format, &minimum)
	if e == sql.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	reason := fmt.Sprintf("已从书库移除：文件小于 %d KiB，原文件保留", minimum/1024)
	if _, e = users.Exec(`UPDATE scanfiles SET status='drop',book_id=0,data=json_set(CASE WHEN json_valid(data) THEN data ELSE '{}' END,'$.skip_reason',?,'$.skip_reason_code','file.too_small') WHERE book_id=? AND (path=? OR lower(name) LIKE ?)`, reason, bookID, path, "%."+strings.ToLower(format)); e != nil {
		return e
	}
	_, e = db.Exec(`UPDATE import_size_audit_removed SET applied=1 WHERE generation=? AND format_id=?`, generation, id)
	return e
}

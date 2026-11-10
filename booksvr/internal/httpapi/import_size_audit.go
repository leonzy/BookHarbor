package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return map[string]any{"state": state, "minimum_kib": minimum / 1024, "total": total, "checked": checked, "removed_formats": removed, "removed_books": books, "unreadable": unreadable, "error": message, "updated_at": updated}
}
func attachImportSizeAudit(parent context.Context, opts ServeOptions, books, users *sql.DB) (ServeOptions, func(), error) {
	if e := library.EnsureImportSizeAudit(books); e != nil {
		return opts, nil, e
	}
	previous := opts.ImportWatchStatus
	opts.ImportWatchStatus = func() map[string]any {
		out := map[string]any{}
		if previous != nil {
			out = previous()
		}
		out["size_audit"] = importSizeAuditStatus(books)
		return out
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		nextIdle := time.Time{}
		var settings map[string]any
		var settingsExpires time.Time
		var revision uint64
		var initializedGeneration string
		var initializedMinimum int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if time.Now().Before(nextIdle) {
				continue
			}
			currentRevision := importSettingsRevision.Load()
			if settings == nil || time.Now().After(settingsExpires) || currentRevision != revision {
				settings = LoadSettings(opts.SettingsPath)
				settingsExpires = time.Now().Add(time.Second)
				revision = currentRevision
			}
			currentOptions := opts
			currentOptions.ImportDir = importDirFromSettings(opts.ImportDir, settings)
			currentOptions.SettingsPath = ""
			requested := settingString(settings, "import_size_audit_requested")
			if requested == "" {
				nextIdle = time.Now().Add(time.Second)
				continue
			}
			minimum := importMinSize(settings) * 1024
			if initializedGeneration != requested || initializedMinimum != minimum {
				if e := runImportSizeAuditStep(ctx, currentOptions, books, users, requested, minimum, true); e != nil {
					books.Exec(`UPDATE import_size_audit SET error=? WHERE id=1`, e.Error())
					nextIdle = time.Now().Add(time.Second)
					continue
				}
				initializedGeneration = requested
				initializedMinimum = minimum
			}
			if previous != nil {
				state := previous()
				if source, ok := state["source_sync"].(map[string]any); ok && source["state"] == "scanning" {
					nextIdle = time.Now().Add(time.Second)
					continue
				}
			}
			work := func() error {
				return runImportSizeAuditStep(ctx, currentOptions, books, users, requested, importMinSize(settings)*1024)
			}
			var e error
			if opts.TryImportMaintenance != nil {
				_, e = opts.TryImportMaintenance(work)
			} else {
				e = work()
			}
			if e != nil {
				books.Exec(`UPDATE import_size_audit SET error=?,updated_at=? WHERE id=1`, e.Error(), time.Now().Unix())
				nextIdle = time.Now().Add(time.Second)
			}
			status := importSizeAuditStatus(books)
			if status["state"] != "running" {
				nextIdle = time.Now().Add(time.Second)
			}
		}
	}()
	return opts, func() { cancel(); <-done }, nil
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
	if state != "running" {
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

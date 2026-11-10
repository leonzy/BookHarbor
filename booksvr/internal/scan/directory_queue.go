package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"talebook/booksvr/internal/userdb"
)

// DirectoryProgress is a durable task summary. Totals grow during discovery;
// they are counts, never a percentage of an unknown filesystem tree.
type DirectoryProgress struct {
	ImportSkipped   int64  `json:"-"`
	ImportFailed    int64  `json:"-"`
	Imported        int64  `json:"imported"`
	RecheckRequired bool   `json:"recheck_required"`
	Phase           string `json:"phase"`
	AutoImport      bool   `json:"auto_import"`
	ID              int64  `json:"id"`
	Scope           string `json:"scope"`
	Mode            string `json:"mode"`
	State           string `json:"state"`
	Current         string `json:"current"`
	Error           string `json:"error"`
	Files           int64  `json:"files"`
	Skipped         int64  `json:"skipped"`
	Ready           int64  `json:"ready"`
	Failed          int64  `json:"failed"`
	Directories     int64  `json:"directories"`
	Enumerated      int64  `json:"enumerated"`
	Pending         int64  `json:"pending"`
	UpdatedAt       int64  `json:"updated_at"`
}

type fullDirectoryCheckKey struct{}

var ErrDirectoryUnavailable = errors.New("导入目录暂不可用，任务保留")

func WithFullDirectoryCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, fullDirectoryCheckKey{}, true)
}
func IsFullDirectoryCheck(ctx context.Context) bool {
	v, _ := ctx.Value(fullDirectoryCheckKey{}).(bool)
	return v
}

func (s *Service) EnsureDirectoryQueue() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS scan_directory_snapshots(path TEXT PRIMARY KEY,stamp TEXT NOT NULL,updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_runs(id INTEGER PRIMARY KEY,scope TEXT NOT NULL,mode TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',current TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',files INTEGER NOT NULL DEFAULT 0,skipped INTEGER NOT NULL DEFAULT 0,ready INTEGER NOT NULL DEFAULT 0,failed INTEGER NOT NULL DEFAULT 0,directories INTEGER NOT NULL DEFAULT 0,enumerated INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS scan_directory_runs_scope ON scan_directory_runs(scope,id DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS scan_directory_active_scope ON scan_directory_runs(scope) WHERE state<>'done'`,
		`CREATE INDEX IF NOT EXISTS scanfiles_path_idx ON scanfiles(path)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_queue(job_id INTEGER NOT NULL,path TEXT NOT NULL,cursor TEXT NOT NULL DEFAULT '',stamp TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'pending',PRIMARY KEY(job_id,path))`,
		`CREATE INDEX IF NOT EXISTS scan_directory_pending ON scan_directory_queue(job_id,state)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_lease(id INTEGER PRIMARY KEY CHECK(id=1),owner TEXT NOT NULL,expires INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_control(id INTEGER PRIMARY KEY CHECK(id=1),stop_generation INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO scan_directory_control(id) VALUES(1)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_events(path TEXT PRIMARY KEY,version INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scan_directory_run_events(job_id INTEGER NOT NULL,path TEXT NOT NULL,version INTEGER NOT NULL,PRIMARY KEY(job_id,path))`,
		`CREATE TABLE IF NOT EXISTS scan_directory_imports(job_id INTEGER NOT NULL,scan_file_id INTEGER NOT NULL,PRIMARY KEY(job_id,scan_file_id))`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			return err
		}
	}
	var hasKind bool
	if err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('scan_directory_queue') WHERE name='kind')`).Scan(&hasKind); err != nil {
		return err
	}
	if !hasKind {
		if _, err := s.DB.Exec(`ALTER TABLE scan_directory_queue ADD COLUMN kind TEXT NOT NULL DEFAULT 'directory'`); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{{"phase", "TEXT NOT NULL DEFAULT 'scanning'"}, {"auto_import", "INTEGER NOT NULL DEFAULT 0"}, {"started_at", "INTEGER NOT NULL DEFAULT 0"}, {"imported", "INTEGER NOT NULL DEFAULT 0"}, {"import_skipped", "INTEGER NOT NULL DEFAULT 0"}, {"import_failed", "INTEGER NOT NULL DEFAULT 0"}} {
		rows, e := s.DB.Query(`PRAGMA table_info(scan_directory_runs)`)
		if e != nil {
			return e
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
				rows.Close()
				return e
			}
			if name == column.name {
				found = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if !found {
			if _, e = s.DB.Exec(`ALTER TABLE scan_directory_runs ADD COLUMN ` + column.name + ` ` + column.definition); e != nil {
				return e
			}
		}
	}
	return nil
}

// PrepareDirectoryScan persists intent before an in-memory worker is queued.
// A stopped/in-flight task is resumed instead of creating another traversal.
func (s *Service) PrepareDirectoryScan(root string, full bool) (int64, error) {
	if err := s.EnsureDirectoryQueue(); err != nil {
		return 0, err
	}
	root = filepath.Clean(root)
	var id int64
	var existingMode string
	err := s.DB.QueryRow(`SELECT id,mode FROM scan_directory_runs WHERE scope=? AND state<>'done' ORDER BY id DESC LIMIT 1`, root).Scan(&id, &existingMode)
	if err == nil {
		if full && existingMode != "full" {
			return 0, fmt.Errorf("有未完成的扫描任务，请先继续扫描")
		}
		tx, e := s.DB.Begin()
		if e != nil {
			return 0, e
		}
		defer tx.Rollback()
		if e = enrollDirectoryEvents(tx, id, root, full); e != nil {
			return 0, e
		}
		return id, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	mode := "incremental"
	if full {
		mode = "full"
	} else {
		var n int
		if err = tx.QueryRow(`SELECT count(*) FROM scan_directory_snapshots WHERE path=?`, root).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			if info, e := os.Stat(root); e != nil || info.IsDir() {
				mode = "baseline"
			}
		}
	}
	r, err := tx.Exec(`INSERT INTO scan_directory_runs(scope,mode,updated_at,started_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, root, mode, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err = r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if n, e := r.RowsAffected(); e != nil {
		return 0, e
	} else if n == 0 {
		if e = tx.QueryRow(`SELECT id FROM scan_directory_runs WHERE scope=? AND state<>'done'`, root).Scan(&id); e != nil {
			return 0, e
		}
		return id, tx.Commit()
	}
	if _, err = tx.Exec(`INSERT INTO scan_directory_queue(job_id,path) VALUES(?,?)`, id, root); err != nil {
		return 0, err
	}
	prefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
	// Checking known directory stamps catches deep changes without re-reading
	// every file. New children are discovered by their changed parent.
	if _, err = tx.Exec(`INSERT OR IGNORE INTO scan_directory_queue(job_id,path) SELECT ?,path FROM scan_directory_snapshots WHERE path>=? AND path<?`, id, prefix, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator+1)); err != nil {
		return 0, err
	}
	if err = enrollDirectoryEvents(tx, id, root, full); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Service) DirectoryProgress() (*DirectoryProgress, error) {
	p := &DirectoryProgress{}
	if err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM scan_directory_events WHERE path LIKE ':full:%')`).Scan(&p.RecheckRequired); err != nil {
		return nil, err
	}
	err := s.DB.QueryRow(`SELECT id,scope,mode,state,current,error,files,skipped,ready,failed,directories,enumerated,updated_at FROM scan_directory_runs ORDER BY CASE state WHEN 'running' THEN 0 WHEN 'paused' THEN 1 WHEN 'waiting' THEN 2 WHEN 'failed' THEN 3 WHEN 'queued' THEN 4 ELSE 5 END,CASE WHEN state='done' THEN -id ELSE id END LIMIT 1`).Scan(&p.ID, &p.Scope, &p.Mode, &p.State, &p.Current, &p.Error, &p.Files, &p.Skipped, &p.Ready, &p.Failed, &p.Directories, &p.Enumerated, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = s.DB.QueryRow(`SELECT phase,auto_import,imported FROM scan_directory_runs WHERE id=?`, p.ID).Scan(&p.Phase, &p.AutoImport, &p.Imported); err != nil {
		return nil, err
	}
	if p.State == "running" {
		var active bool
		if e := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM scan_directory_lease WHERE id=1 AND expires>?)`, time.Now().Unix()).Scan(&active); e != nil {
			return nil, e
		}
		if !active {
			p.State = "queued"
		}
	}
	err = s.DB.QueryRow(`SELECT count(*) FROM scan_directory_queue WHERE job_id=? AND state='pending'`, p.ID).Scan(&p.Pending)
	return p, err
}

func (s *Service) PendingDirectoryScopes() ([]string, error) {
	rows, err := s.DB.Query(`SELECT scope FROM scan_directory_runs WHERE state<>'done' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

func (s *Service) ExternalDirectoryScanActive() bool {
	var active bool
	_ = s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM scan_directory_lease WHERE expires>? AND owner NOT LIKE ?)`, time.Now().Unix(), fmt.Sprintf("%d:%%", os.Getpid())).Scan(&active)
	return active
}

func directoryStamp(info os.FileInfo) string {
	// Device numbers are deliberately absent: remounts may change them.
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

func (s *Service) saveDirectoryProgress(tx *sql.Tx, p *DirectoryProgress) error {
	p.UpdatedAt = time.Now().Unix()
	_, err := tx.Exec(`UPDATE scan_directory_runs SET state=?,current=?,error=?,files=?,skipped=?,ready=?,failed=?,directories=?,enumerated=?,updated_at=?,phase=?,auto_import=?,imported=?,import_skipped=?,import_failed=? WHERE id=?`, p.State, p.Current, p.Error, p.Files, p.Skipped, p.Ready, p.Failed, p.Directories, p.Enumerated, p.UpdatedAt, p.Phase, p.AutoImport, p.Imported, p.ImportSkipped, p.ImportFailed, p.ID)
	return err
}

// ScanDirectoryQueue reads at most the changed/current directory again on
// resume. Previously completed directories and file payloads are not replayed.
func (s *Service) ScanDirectoryQueue(ctx context.Context, root string, full bool, batchSize int, afterBatch func() error) (err error) {
	s.directoryMu.Lock()
	defer s.directoryMu.Unlock()
	if err = s.EnsureDirectoryQueue(); err != nil {
		return err
	}
	var stopGeneration int64
	if err = s.DB.QueryRow(`SELECT stop_generation FROM scan_directory_control WHERE id=1`).Scan(&stopGeneration); err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixNano())
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		r, e := s.DB.Exec(`INSERT INTO scan_directory_lease(id,owner,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE scan_directory_lease.expires<?`, owner, time.Now().Unix()+60, time.Now().Unix())
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n == 1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	leaseDone := make(chan struct{})
	leaseStopped := make(chan struct{})
	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	ctx = leaseCtx
	go func() {
		defer close(leaseStopped)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-leaseDone:
				return
			case <-tick.C:
				var generation int64
				if e := s.DB.QueryRow(`SELECT stop_generation FROM scan_directory_control WHERE id=1`).Scan(&generation); e != nil || generation != stopGeneration {
					cancelLease()
					continue
				}
				r, e := s.DB.Exec(`UPDATE scan_directory_lease SET expires=? WHERE id=1 AND owner=?`, time.Now().Unix()+60, owner)
				if e != nil {
					cancelLease()
					continue
				}
				if n, e := r.RowsAffected(); e != nil || n != 1 {
					cancelLease()
				}
			}
		}
	}()
	defer func() {
		close(leaseDone)
		<-leaseStopped
		s.DB.Exec(`DELETE FROM scan_directory_lease WHERE id=1 AND owner=?`, owner)
	}()
	id, err := s.PrepareDirectoryScan(root, full)
	if err != nil {
		return err
	}
	// The lease is exclusive. Any other persisted running row belongs to an
	// interrupted process; leave its cursor queued rather than showing it live.
	if _, err = s.DB.Exec(`UPDATE scan_directory_runs SET state='queued' WHERE state='running' AND id<>?`, id); err != nil {
		return err
	}
	p := &DirectoryProgress{ID: id, Phase: "scanning", AutoImport: afterBatch != nil}
	var previousState string
	err = s.DB.QueryRow(`SELECT scope,mode,state,files,skipped,ready,failed,directories,enumerated,imported,import_skipped,import_failed FROM scan_directory_runs WHERE id=?`, id).Scan(&p.Scope, &p.Mode, &previousState, &p.Files, &p.Skipped, &p.Ready, &p.Failed, &p.Directories, &p.Enumerated, &p.Imported, &p.ImportSkipped, &p.ImportFailed)
	if err != nil {
		return err
	}
	p.State = "running"
	s.directoryTask.Store(&activeDirectoryTask{id: id, scope: p.Scope})
	defer s.directoryTask.Store(nil)
	if _, err = s.DB.Exec(`UPDATE scan_directory_runs SET state='running',error='',updated_at=?,auto_import=?,phase='scanning' WHERE id=?`, time.Now().Unix(), p.AutoImport, id); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			p.State = "failed"
			p.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				p.State = "paused"
				p.Error = ""
			} else if os.IsNotExist(err) || os.IsPermission(err) || errors.Is(err, ErrDirectoryUnavailable) {
				p.State = "waiting"
			}
		}
		tx, e := s.DB.Begin()
		if e == nil {
			e = s.saveDirectoryProgress(tx, p)
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
		}
		if err == nil {
			err = e
		}
	}()
	if batchSize < 1 || batchSize > 500 {
		batchSize = 100
	}
	if s.CheckDirectoryRoot != nil {
		if err = s.CheckDirectoryRoot(ctx, p.Scope); err != nil {
			return err
		}
	}
	if p.Mode == "full" {
		r, e := s.RecheckCompatibilityFailures(ctx, []string{p.Scope})
		if e != nil {
			return e
		}
		p.Ready += int64(r.Recovered)
	}
	flushImport := func() error {
		if afterBatch == nil {
			return nil
		}
		if s.CheckDirectoryRoot != nil {
			if e := s.CheckDirectoryRoot(ctx, p.Scope); e != nil {
				return e
			}
		}
		p.Phase = "importing"
		if _, e := s.DB.Exec(`UPDATE scan_directory_runs SET phase='importing',auto_import=1 WHERE id=?`, p.ID); e != nil {
			return e
		}
		e := afterBatch()
		p.Phase = "scanning"
		var skipped, failed int64
		countErr := s.DB.QueryRow(`SELECT count(DISTINCT CASE WHEN f.status IN ('indexed','imported') AND f.book_id>0 THEN f.book_id END),coalesce(sum(CASE WHEN f.status IN ('exist','drop') THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN f.status IN ('failed','delete_failed') THEN 1 ELSE 0 END),0) FROM scan_directory_imports j JOIN scanfiles f ON f.id=j.scan_file_id WHERE j.job_id=?`, id).Scan(&p.Imported, &skipped, &failed)
		if countErr == nil {
			p.Skipped += skipped - p.ImportSkipped
			p.Failed += failed - p.ImportFailed
			p.ImportSkipped = skipped
			p.ImportFailed = failed
			tx, beginErr := s.DB.Begin()
			countErr = beginErr
			if countErr == nil {
				countErr = s.saveDirectoryProgress(tx, p)
				if countErr == nil {
					countErr = tx.Commit()
				} else {
					tx.Rollback()
				}
			}
		}
		if e == nil {
			e = countErr
		}
		if _, saveErr := s.DB.Exec(`UPDATE scan_directory_runs SET phase='scanning' WHERE id=?`, p.ID); e == nil {
			e = saveErr
		}
		return e
	}
	sinceImport := 0
	if afterBatch != nil {
		var ready bool
		if err = s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM scanfiles WHERE status IN ('ready','queued','importing'))`).Scan(&ready); err != nil {
			return err
		}
		if ready {
			if err = flushImport(); err != nil {
				return err
			}
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		if s.CheckDirectoryRoot != nil {
			if err = s.CheckDirectoryRoot(ctx, p.Scope); err != nil {
				return err
			}
		}
		var dir, cursor, stamp string
		err = s.DB.QueryRow(`SELECT path,cursor,stamp FROM scan_directory_queue WHERE job_id=? AND state='pending' ORDER BY CASE kind WHEN 'archive' THEN 1 ELSE 0 END,rowid LIMIT 1`, id).Scan(&dir, &cursor, &stamp)
		if err == sql.ErrNoRows {
			p.State = "done"
			p.Current = ""
			p.Error = ""
			if p.Mode == "full" {
				tx, e := s.DB.Begin()
				if e != nil {
					return e
				}
				e = ackDirectoryEvent(tx, p.ID, ":full:"+p.Scope)
				if e == nil {
					e = tx.Commit()
				} else {
					tx.Rollback()
				}
				if e != nil {
					return e
				}
			}
			if afterBatch != nil {
				return flushImport()
			}
			return nil
		}
		if err != nil {
			return err
		}
		p.Current = dir
		info, e := os.Stat(dir)
		if e != nil {
			if os.IsNotExist(e) && dir != p.Scope {
				p.Failed++
				if _, e = s.DB.Exec(`UPDATE scan_directory_queue SET state='done' WHERE job_id=? AND path=?`, id, dir); e != nil {
					return e
				}
				continue
			}
			return e
		}
		if !info.IsDir() {
			readyBefore := p.Ready
			if e = s.scanQueuedFile(ctx, dir, p, flushImport); e != nil {
				return e
			}
			p.Files++
			if _, e = s.DB.Exec(`UPDATE scan_directory_queue SET state='done' WHERE job_id=? AND path=?`, id, dir); e != nil {
				return e
			}
			tx, e := s.DB.Begin()
			if e != nil {
				return e
			}
			e = ackDirectoryEvent(tx, id, dir)
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
			if e != nil {
				return e
			}
			if afterBatch != nil && p.Ready > readyBefore {
				if e = flushImport(); e != nil {
					return e
				}
			}
			continue
		}
		currentStamp := fmt.Sprintf("%s:min=%d", directoryStamp(info), s.minimumFileSize())
		var forced bool
		if e = s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM scan_directory_run_events WHERE job_id=? AND path=?)`, id, dir).Scan(&forced); e != nil {
			return e
		}
		if forced && previousState != "running" && cursor != "" {
			cursor = ""
		}
		var oldStamp string
		e = s.DB.QueryRow(`SELECT stamp FROM scan_directory_snapshots WHERE path=?`, dir).Scan(&oldStamp)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if cursor == "" && p.Mode != "full" && oldStamp == currentStamp && !forced {
			p.Directories++
			if _, e = s.DB.Exec(`UPDATE scan_directory_queue SET state='done' WHERE job_id=? AND path=?`, id, dir); e != nil {
				return e
			}
			continue
		}
		if stamp != currentStamp {
			cursor = ""
			stamp = currentStamp
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		p.Enumerated++
		checkpoint := func(done bool) error {
			tx, e := s.DB.Begin()
			if e != nil {
				return e
			}
			defer tx.Rollback()
			state := "pending"
			if done {
				if e = ackDirectoryEvent(tx, id, dir); e != nil {
					return e
				}
				state = "done"
			}
			if _, e = tx.Exec(`UPDATE scan_directory_queue SET cursor=?,stamp=?,state=? WHERE job_id=? AND path=?`, cursor, stamp, state, id, dir); e != nil {
				return e
			}
			if done {
				if _, e = tx.Exec(`INSERT INTO scan_directory_snapshots(path,stamp,updated_at) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET stamp=excluded.stamp,updated_at=excluded.updated_at`, dir, stamp, time.Now().Unix()); e != nil {
					return e
				}
			}
			if e = s.saveDirectoryProgress(tx, p); e != nil {
				return e
			}
			if e := tx.Commit(); e != nil {
				return e
			}
			if s.MaintenanceCheckpoint != nil && ctx.Err() == nil {
				s.MaintenanceCheckpoint(ctx)
			}
			return nil
		}
		steps := 0
		lastCheckpoint := time.Now()
		for _, entry := range entries {
			if entry.Name() <= cursor {
				continue
			}
			if e = ctx.Err(); e != nil {
				if saveErr := checkpoint(false); saveErr != nil {
					return saveErr
				}
				return e
			}
			path := filepath.Join(dir, entry.Name())
			p.Current = path
			if !strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(entry.Name(), "@__") && entry.Type()&os.ModeSymlink == 0 && path != s.LibraryRoot && path != s.archiveCacheRoot() {
				if entry.IsDir() {
					_, e = s.DB.Exec(`INSERT OR IGNORE INTO scan_directory_queue(job_id,path) VALUES(?,?)`, id, path)
				} else {
					format := s.queueFormat(path)
					if format != "" {
						if format == "zip" || format == "rar" {
							_, e = s.DB.Exec(`INSERT OR IGNORE INTO scan_directory_queue(job_id,path,kind) VALUES(?,?,'archive')`, id, path)
						} else {
							e = s.scanQueuedFile(ctx, path, p, nil)
							if e == nil {
								p.Files++
								sinceImport++
							}
						}
					}
				}
				if e != nil {
					if saveErr := checkpoint(false); saveErr != nil {
						return saveErr
					}
					return e
				}
			}
			cursor = entry.Name()
			steps++
			if steps%32 == 0 || time.Since(lastCheckpoint) > 2*time.Second {
				if e = checkpoint(false); e != nil {
					return e
				}
				lastCheckpoint = time.Now()
			}
			if afterBatch != nil && sinceImport >= batchSize {
				if e = checkpoint(false); e != nil {
					return e
				}
				if e = flushImport(); e != nil {
					return e
				}
				sinceImport = 0
			}
		}
		p.Directories++
		if e = checkpoint(true); e != nil {
			return e
		}
		if afterBatch != nil && sinceImport > 0 {
			if e = flushImport(); e != nil {
				return e
			}
			sinceImport = 0
		}
	}
}

func (s *Service) queueFormat(path string) string {
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if s.SupportedFormats[format] {
		return format
	}
	// Only known truncated extensions are probed; arbitrary non-book files are
	// not read. Never rename or copy the original source.
	if format == "a" || format == "az" || filepath.Ext(path) == "." {
		f, e := os.Open(path)
		if e != nil {
			return ""
		}
		defer f.Close()
		var header [68]byte
		if _, e = f.ReadAt(header[:], 0); e == nil && string(header[60:68]) == "BOOKMOBI" {
			return "azw3"
		}
	}
	return ""
}

func (s *Service) scanQueuedFile(ctx context.Context, path string, p *DirectoryProgress, archiveCheckpoint func() error) error {
	format := s.queueFormat(path)
	if format == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if format == "zip" {
		r := Result{ScanID: time.Now().Unix()}
		prior := Result{}
		lastProcessed := s.Progress().Processed
		lastBatch := lastProcessed
		lastSave := time.Now()
		update := func(force bool) error {
			processed := s.Progress().Processed
			if !force && processed-lastProcessed < 32 && time.Since(lastSave) < 2*time.Second {
				return nil
			}
			p.Files += int64(processed - lastProcessed)
			lastProcessed = processed
			p.Ready += int64(r.Ready - prior.Ready)
			p.Skipped += int64(r.Skipped + r.Dropped + r.Exist - prior.Skipped - prior.Dropped - prior.Exist)
			p.Failed += int64(r.Failed - prior.Failed)
			prior = r
			p.Current = s.Progress().Current
			tx, e := s.DB.Begin()
			if e != nil {
				return e
			}
			e = s.saveDirectoryProgress(tx, p)
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
			if e != nil {
				return e
			}
			lastSave = time.Now()
			if archiveCheckpoint != nil && (force || processed-lastBatch >= 50) {
				lastBatch = processed
				return archiveCheckpoint()
			}
			return nil
		}
		err = s.scanZIP(ctx, path, &r, map[string]bool{}, func() error { return update(false) })
		if e := update(true); err == nil {
			err = e
		}
		return err
	}
	if s.belowMinimum(info.Size()) {
		p.Skipped++
		return nil
	}
	changed, err := s.changedSource(path, info)
	if err != nil {
		return err
	}
	if !changed {
		p.Skipped++
		return nil
	}
	rows, err := s.registerTasks([]Task{{Name: filepath.Base(path), Path: path, Format: format}}, time.Now().Unix())
	if err != nil {
		return err
	}
	for _, row := range rows {
		if format != strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".") {
			setRowData(row, "detected_format", format)
		}
		status, e := s.processRow(row)
		if e != nil {
			return e
		}
		switch status {
		case userdb.ScanStatusReady:
			p.Ready++
		case userdb.ScanStatusDrop, userdb.ScanStatusExist:
			p.Skipped++
		case userdb.ScanStatusFailed:
			p.Failed++
		}
	}
	return nil
}

func scanRowFormat(row *userdb.ScanFile, path string) string {
	if detected, ok := row.Data["detected_format"].(string); ok && detected == "azw3" {
		return detected
	}
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
}

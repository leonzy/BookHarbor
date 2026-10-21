package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/scan"
	"talebook/booksvr/internal/userdb"
)

// attachImportWorker serializes writes from manual and automatic imports. The
// watcher reads persisted settings on every tick, including after a restart.
func attachImportWorker(ctx context.Context, opts ServeOptions, db *sql.DB) (ServeOptions, func()) {
	if opts.ScanService == nil || opts.StartScan == nil || opts.StartImport == nil || opts.SettingsPath == "" {
		return opts, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	if db == nil {
		if service := opts.ScanService(); service != nil {
			db = service.DB
		}
	}
	if db == nil {
		cancel()
		return opts, func() {}
	}
	if err := ensureImportTasks(db); err != nil {
		cancel()
		return opts, func() {}
	}
	if err := scan.EnsureQueryIndexes(db); err != nil {
		log.Printf("scan: 创建导入查询索引失败，保留导入任务并继续运行: %v", err)
	}
	var workMu, statusMu sync.Mutex
	var activeCancel context.CancelFunc
	var generation uint64
	var paused bool
	db.QueryRow(`SELECT paused FROM import_control WHERE id=1`).Scan(&paused)
	status := map[string]any{"state": "off", "queued": 0, "running": 0, "failed": 0, "last_scan_at": nil, "message": ""}
	// Existing scan rows survive restarts, so do not claim an already populated
	// library has never been scanned merely because the worker was restarted.
	var lastScan sql.NullString
	if db != nil && db.QueryRow(`SELECT MAX(create_time) FROM scanfiles`).Scan(&lastScan) == nil && lastScan.Valid {
		status["last_scan_at"] = lastScan.String
	}
	update := func(state, message string, running, failed int) {
		statusMu.Lock()
		defer statusMu.Unlock()
		status["state"], status["message"] = state, message
		status["running"], status["failed"] = running, failed
		if state == "scanning" {
			status["last_scan_at"] = time.Now().Format(time.RFC3339)
		}
	}
	opts.ImportWatchStatus = func() map[string]any {
		statusMu.Lock()
		defer statusMu.Unlock()
		copy := map[string]any{"paused": paused}
		for k, v := range status {
			copy[k] = v
		}
		if service := opts.ScanService(); service != nil {
			copy["scan_progress"] = service.Progress()
		}
		return copy
	}
	startScan, startImport := opts.StartScan, opts.StartImport
	if opts.StartImportContext != nil {
		startImport = func(hashes []string, id int64, remove bool, mode string) error {
			return opts.StartImportContext(ctx, hashes, id, remove, mode)
		}
	}
	if opts.StartScanContext != nil {
		startScan = func(path string, batchSize int) error { return opts.StartScanContext(ctx, path, batchSize) }
	}
	type job struct {
		kind       string
		run        func(context.Context) error
		generation uint64
	}
	jobs := make(chan job, 32)
	manualDone := make(chan struct{})
	pending := map[string]int{"scan": 0, "import": 0}
	pendingGeneration := map[string]uint64{}
	enqueue := func(kind string, run func(context.Context) error) error {
		statusMu.Lock()
		defer statusMu.Unlock()
		if kind == "library_import" && pending[kind] > 0 && pendingGeneration[kind] == generation {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		select {
		case jobs <- job{kind, run, generation}:
			pending[kind]++
			pendingGeneration[kind] = generation
			status["manual_"+kind+"_running"] = true
			status["manual_"+kind+"_error"] = ""
			if kind == "library_import" {
				status["manual_library_import_phase"] = "queued"
			}
			return nil
		default:
			return fmt.Errorf("已有过多导入任务排队，请等待当前任务完成")
		}
	}
	go func() {
		defer close(manualDone)
		for {
			select {
			case <-ctx.Done():
				return
			case task := <-jobs:
				workMu.Lock()
				if ctx.Err() != nil {
					workMu.Unlock()
					return
				}
				statusMu.Lock()
				taskCtx, taskCancel := context.WithCancel(ctx)
				if task.generation != generation {
					taskCancel()
				}
				activeCancel = taskCancel
				statusMu.Unlock()
				err := task.run(taskCtx)
				taskCancel()
				statusMu.Lock()
				activeCancel = nil
				statusMu.Unlock()
				statusMu.Lock()
				sameGeneration := task.generation == generation
				statusMu.Unlock()
				if task.kind == "library_import" && ctx.Err() == nil && sameGeneration {
					if errors.Is(err, context.Canceled) {
						_, _ = db.Exec(`UPDATE import_resume_job SET state='cancelled',error='' WHERE id=1 AND state='active'`)
					} else if err == nil {
						_, err = db.Exec(`DELETE FROM import_resume_job WHERE id=1`)
					} else {
						db.Exec(`UPDATE import_resume_job SET state='failed',error=? WHERE id=1`, err.Error())
					}
				}
				workMu.Unlock()
				statusMu.Lock()
				if task.kind == "scan" {
					status["last_scan_at"] = time.Now().Format(time.RFC3339)
				}
				pending[task.kind]--
				status["manual_"+task.kind+"_running"] = pending[task.kind] > 0
				if err != nil && !errors.Is(err, context.Canceled) {
					status["manual_"+task.kind+"_error"] = err.Error()
				}
				statusMu.Unlock()
			}
		}
	}()
	// Explicit manual work resumes automatic monitoring; only the stop action persists a pause.
	resume := func() error {
		statusMu.Lock()
		defer statusMu.Unlock()
		if _, err := db.Exec(`UPDATE import_control SET paused=0 WHERE id=1`); err != nil {
			return err
		}
		paused = false
		return nil
	}
	opts.CancelImportTask = func() error {
		statusMu.Lock()
		defer statusMu.Unlock()
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE import_control SET paused=1 WHERE id=1`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE import_resume_job SET state='cancelled',error='' WHERE id=1`); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		paused = true
		generation++
		if activeCancel != nil {
			activeCancel()
		}
		return nil
	}
	scanWith := func(c context.Context, path string, n int) error {
		if err := c.Err(); err != nil {
			return err
		}
		if opts.StartScanContext != nil {
			return opts.StartScanContext(c, path, n)
		}
		return startScan(path, n)
	}
	importWith := func(c context.Context, h []string, id int64, remove bool, mode string) error {
		if err := c.Err(); err != nil {
			return err
		}
		if opts.StartImportContext != nil {
			return opts.StartImportContext(c, h, id, remove, mode)
		}
		return startImport(h, id, remove, mode)
	}
	opts.StartScan = func(path string, n int) error {
		if err := resume(); err != nil {
			return err
		}
		return enqueue("scan", func(c context.Context) error { return scanWith(c, path, n) })
	}
	opts.StartImport = func(h []string, id int64, remove bool, mode string) error {
		if err := resume(); err != nil {
			return err
		}
		selected := append([]string(nil), h...)
		return enqueue("import", func(c context.Context) error { return importWith(c, selected, id, remove, mode) })
	}
	opts.StartLibraryImport = func(id int64, mode string) error {
		if err := resume(); err != nil {
			return err
		}
		if err := rememberImportTask(db, id, mode); err != nil {
			return err
		}
		return enqueue("library_import", func(c context.Context) error {
			phase := func(v string) { statusMu.Lock(); status["manual_library_import_phase"] = v; statusMu.Unlock() }
			return runLibraryImport(c, opts, db, LoadSettings(opts.SettingsPath), id, mode, func(p string, n int) error { return scanWith(c, p, n) }, func(h []string, id int64, b bool, m string) error { return importWith(c, h, id, b, m) }, phase)
		})
	}
	var resumeID int64
	var resumeMode string
	if !paused && db.QueryRow(`SELECT user_id,mode FROM import_resume_job WHERE id=1 AND state='active'`).Scan(&resumeID, &resumeMode) == nil {
		opts.StartLibraryImport(resumeID, resumeMode)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var next time.Time
		var lastStamp [2]settingsFileStamp
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			statusMu.Lock()
			isPaused := paused
			statusMu.Unlock()
			if isPaused {
				update("paused", "已停止，点击扫描或全部导入可继续", 0, 0)
				continue
			}
			settings := LoadSettings(opts.SettingsPath)
			if !settingBool(settings, "import_auto_watch_enabled", false) {
				update("off", "", 0, 0)
				next = time.Time{}
				continue
			}
			stamp := settingsStamp(opts.SettingsPath)
			if stamp == lastStamp && time.Now().Before(next) {
				continue
			}
			lastStamp = stamp
			interval, _ := scan.WatchInterval(toIntSetting(settings, "import_watch_interval_seconds", 30), 500, 30, 500)
			update("scanning", "", 1, 0)
			workMu.Lock()
			statusMu.Lock()
			autoCtx, autoCancel := context.WithCancel(ctx)
			if paused {
				autoCancel()
			}
			activeCancel = autoCancel
			statusMu.Unlock()
			err := runAutoImport(autoCtx, opts, db, settings, func(p string, n int) error { return scanWith(autoCtx, p, n) }, func(hashes []string, id int64, remove bool, mode string) error {
				update("importing", "", 1, 0)
				return importWith(autoCtx, hashes, id, remove, mode)
			}, func(phase string) { update(phase, "", 1, 0) })
			autoCancel()
			statusMu.Lock()
			activeCancel = nil
			statusMu.Unlock()
			workMu.Unlock()
			next = time.Now().Add(time.Duration(interval) * time.Second)
			if errors.Is(err, context.Canceled) {
				update("paused", "已停止，已完成结果保留", 0, 0)
			} else if err != nil {
				update("failed", err.Error(), 0, 1)
			} else {
				update("watching", "等待下一次扫描", 0, 0)
			}
		}
	}()
	return opts, func() { cancel(); <-done; <-manualDone }
}

func runAutoImport(ctx context.Context, opts ServeOptions, db *sql.DB, settings map[string]any,
	startScan func(string, int) error, startImport func([]string, int64, bool, string) error, phases ...func(string)) error {
	id := int64(toIntSetting(settings, "import_watch_user_id", 0))
	if id <= 0 {
		return fmt.Errorf("请重新保存自动监控设置以确认导入管理员")
	}
	var phase func(string)
	if len(phases) > 0 {
		phase = phases[0]
	}
	return runLibraryImport(ctx, opts, db, settings, id, "", startScan, startImport, phase)
}

// runLibraryImport drains existing discoveries and imports each scan batch, all
// under the caller's writer lock. The UI is not part of this task's lifetime.
func runLibraryImport(ctx context.Context, opts ServeOptions, db *sql.DB, settings map[string]any, id int64, mode string,
	startScan func(string, int) error, startImport func([]string, int64, bool, string) error, phase func(string)) error {
	reader, err := userdb.GetReaderByID(db, id)
	if err != nil || reader == nil || !reader.Admin {
		return fmt.Errorf("导入的管理员账号不可用")
	}
	dir := LiveImportDir(opts.ImportDir, opts.SettingsPath)
	if mode != "" {
		dir.ImportMode, dir.SavedImportMode = mode, mode
	}
	roots := importdir.ConfiguredAllowedRoots(dir)
	if service := opts.ScanService(); service != nil {
		if _, err := service.RecheckCompatibilityFailures(ctx, roots); err != nil {
			return err
		}
	}
	setPhase := func(value string) {
		if phase != nil {
			phase(value)
		}
	}
	drain := func() error {
		var afterID int64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			rows, err := opts.ScanService().RecoveredReadyBatch(roots, 500)
			priority := len(rows) > 0
			if err == nil && !priority {
				rows, err = opts.ScanService().ReadyBatch(afterID, 500)
			}
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			var hashes []string
			for _, row := range rows {
				if !priority {
					afterID = row.ID
				}
				for _, root := range roots {
					if importdir.IsWithin(row.Path, root) {
						hashes = append(hashes, row.Hash)
						break
					}
				}
			}
			if len(hashes) > 0 {
				setPhase("importing")
				if err := startImport(hashes, id, false, normalizeImportMode(dir)); err != nil {
					return err
				}
			}
		}
	}
	// The injected legacy scanner may not support checkpoints. Its compatibility
	// path scans once then drains once; production always wires the callback.
	if opts.StartScanBatchContext != nil {
		if err := drain(); err != nil {
			return err
		}
	}
	var problems []error
	scanned := make([]string, 0, len(roots))
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		covered := false
		for _, previous := range scanned {
			if importdir.IsWithin(root, previous) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if check := importdir.CheckImportDirectory(root, dir); check.Status == "error" {
			problems = append(problems, fmt.Errorf("%s: %s", root, check.Msg))
			continue
		}
		scanned = append(scanned, root)
		setPhase("scanning")
		if opts.StartScanBatchContext != nil {
			err = opts.StartScanBatchContext(ctx, root, dir.ScanBatchSize, func() error {
				if err := drain(); err != nil {
					return err
				}
				setPhase("scanning")
				return nil
			})
		} else {
			err = startScan(root, dir.ScanBatchSize)
		}
		if err != nil {
			return err
		}
	}
	if err := drain(); err != nil {
		return err
	}
	return errors.Join(problems...)
}

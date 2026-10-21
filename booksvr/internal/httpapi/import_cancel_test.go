package httpapi

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/scan"
	"testing"
	"time"
)

func TestManualImportStopPersistsAndCanResume(t *testing.T) {
	db := scanTestDB(t)
	svc := scan.NewService(db, nil)
	var calls atomic.Int64
	started := make(chan struct{}, 3)
	path := filepath.Join(t.TempDir(), "auto.py")
	if err := SaveSettings(path, map[string]any{"import_auto_watch_enabled": false}); err != nil {
		t.Fatal(err)
	}
	opts := ServeOptions{SettingsPath: path, ScanService: func() *scan.Service { return svc }, StartScan: func(string, int) error { return nil }, StartImport: func([]string, int64, bool, string) error { return nil }, StartScanContext: func(ctx context.Context, _ string, _ int) error {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	running, stop := attachImportWorker(context.Background(), opts, db)
	if err := running.StartScan("/books", 500); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	if err := running.CancelImportTask(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for running.ImportWatchStatus()["manual_scan_running"] == true && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running.ImportWatchStatus()["paused"] != true {
		t.Fatal("pause absent")
	}
	stop()
	running, stop = attachImportWorker(context.Background(), opts, db)
	defer stop()
	if running.ImportWatchStatus()["paused"] != true {
		t.Fatal("pause lost after restart")
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled job restarted")
	}
	if err := running.StartScan("/books", 500); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("explicit resume did not start")
	}
	if running.ImportWatchStatus()["paused"] != false {
		t.Fatal("explicit resume still paused")
	}
}

func TestLibraryImportStopDoesNotReviveAndImmediateRetryIsQueued(t *testing.T) {
	users := newUserFixture(t)
	id := seedReader(t, users.DB, "cancel-admin", "secret", true, true)
	svc := scan.NewService(scanTestDB(t), nil)
	root := t.TempDir()
	settings := filepath.Join(t.TempDir(), "auto.py")
	if err := SaveSettings(settings, map[string]any{"import_allowed_roots": []string{root}, "scan_upload_path": root}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	var calls atomic.Int64
	opts := ServeOptions{SettingsPath: settings, ScanService: func() *scan.Service { return svc }, ImportDir: importdir.Config{AllowedRoots: []string{root}}, StartScan: func(string, int) error { return nil }, StartImport: func([]string, int64, bool, string) error { return nil }, StartScanBatchContext: func(ctx context.Context, _ string, _ int, _ func() error) error {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	running, stop := attachImportWorker(context.Background(), opts, users.DB)
	wait := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("full task did not start")
		}
	}
	if err := running.StartLibraryImport(id, "index"); err != nil {
		t.Fatal(err)
	}
	wait()
	if err := running.CancelImportTask(); err != nil {
		t.Fatal(err)
	}
	if err := running.StartLibraryImport(id, "index"); err != nil {
		t.Fatal(err)
	}
	wait()
	if err := running.CancelImportTask(); err != nil {
		t.Fatal(err)
	}
	stop()
	var state string
	if err := users.DB.QueryRow(`SELECT state FROM import_resume_job WHERE id=1`).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	running, stop = attachImportWorker(context.Background(), opts, users.DB)
	defer stop()
	if running.ImportWatchStatus()["paused"] != true || calls.Load() != 2 {
		t.Fatal("cancelled intent revived")
	}
}

package scan

import (
	"context"
	"testing"
	"time"
)

func TestImportMaintenanceRunsAfterTransactionCommit(t *testing.T) {
	s := newImportService(t, importTestDB(t), importLibraryDB(t))
	s.DB.SetMaxOpenConns(1)
	file := makeValidEpub(t, t.TempDir(), "checkpoint.epub")
	seedReadyRow(t, s.DB, file, "checkpoint")
	called := 0
	s.MaintenanceCheckpoint = func(c context.Context) {
		called++
		ctx, cancel := context.WithTimeout(c, time.Second)
		defer cancel()
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal("import transaction still held", e)
		}
		defer tx.Rollback()
		var n int
		if e = tx.QueryRow(`SELECT count(*) FROM scanfiles WHERE status='indexed'`).Scan(&n); e != nil || n != 1 {
			t.Fatal("uncommitted import visible to maintenance", n, e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	result, e := s.ImportContext(context.Background(), nil, 1, false, ModeIndex)
	if e != nil || result.Indexed != 1 || called != 1 {
		t.Fatal(result, called, e)
	}
}
func TestScanMaintenanceRunsAfterDurableCheckpoint(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	s.DB.SetMaxOpenConns(1)
	root := t.TempDir()
	writeBook(t, root, "chapter.txt", []byte("book text"))
	called := 0
	s.MaintenanceCheckpoint = func(c context.Context) {
		called++
		ctx, cancel := context.WithTimeout(c, time.Second)
		defer cancel()
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal("directory checkpoint still held", e)
		}
		defer tx.Rollback()
		var n int
		if e = tx.QueryRow(`SELECT count(*) FROM scan_directory_runs`).Scan(&n); e != nil || n < 1 {
			t.Fatal(n, e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.ScanDirectoryQueue(context.Background(), root, false, 10, nil); e != nil {
		t.Fatal(e)
	}
	if called == 0 {
		t.Fatal("maintenance never offered a scan checkpoint")
	}
}

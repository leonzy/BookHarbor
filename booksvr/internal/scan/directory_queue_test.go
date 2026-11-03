package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectoryQueueIncrementalAndExplicitFull(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	root := t.TempDir()
	sub := filepath.Join(root, "leaf")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	writeBook(t, sub, "a.txt", []byte("first book"))
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.DirectoryProgress()
	if err != nil || p.State != "done" || p.Files != 1 || p.Enumerated != 2 {
		t.Fatal(p, err)
	}
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.DirectoryProgress()
	if p.Mode != "incremental" || p.Enumerated != 0 || p.Files != 0 || p.Directories != 2 {
		t.Fatal("unchanged directories enumerated", p)
	}
	writeBook(t, sub, "b.txt", []byte("second different book"))
	future := time.Now().Add(time.Second)
	os.Chtimes(sub, future, future)
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.DirectoryProgress()
	if p.Enumerated != 1 || p.Ready != 1 || p.Skipped != 1 {
		t.Fatal(p)
	}
	if err := s.ScanDirectoryQueue(context.Background(), root, true, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.DirectoryProgress()
	if p.Mode != "full" || p.Enumerated != 2 || p.Files != 2 || p.Skipped != 2 {
		t.Fatal(p)
	}
}

func TestDirectoryQueuePauseRecreateAndChangedPausedDirectory(t *testing.T) {
	db := testDB(t)
	s := directoryTestConnections(newTestService(db))
	root := t.TempDir()
	writeBook(t, root, "b.txt", []byte("book b"))
	writeBook(t, root, "c.txt", []byte("book c"))
	ctx, cancel := context.WithCancel(context.Background())
	err := s.ScanDirectoryQueue(ctx, root, false, 1, func() error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p, _ := s.DirectoryProgress()
	id := p.ID
	if p.State != "paused" || p.Files != 1 {
		t.Fatal(p)
	}
	// A new name before the saved cursor must not be lost after resume.
	writeBook(t, root, "a.txt", []byte("book a"))
	future := time.Now().Add(time.Second)
	os.Chtimes(root, future, future)
	restarted := directoryTestConnections(newTestService(db))
	if err := restarted.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = restarted.DirectoryProgress()
	if p.ID != id || p.State != "done" {
		t.Fatal(p)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM scanfiles`).Scan(&n)
	if n != 3 {
		t.Fatal(n)
	}
	if row, _ := getScanFileByPath(db, filepath.Join(root, "a.txt")); row == nil {
		t.Fatal("new file before cursor missed")
	}
}

func TestDirectoryQueueOfflineDoesNotReplaceSnapshot(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	root := filepath.Join(t.TempDir(), "books")
	os.Mkdir(root, 0700)
	writeBook(t, root, "a.txt", []byte("book"))
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	var before string
	s.DB.QueryRow(`SELECT stamp FROM scan_directory_snapshots WHERE path=?`, root).Scan(&before)
	os.Rename(root, root+".offline")
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	p, _ := s.DirectoryProgress()
	if p.State != "waiting" || p.Pending == 0 {
		t.Fatal(p)
	}
	var after string
	s.DB.QueryRow(`SELECT stamp FROM scan_directory_snapshots WHERE path=?`, root).Scan(&after)
	if before != after {
		t.Fatal("offline snapshot overwritten")
	}
	os.Rename(root+".offline", root)
	if err := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryQueuePresentButProtectedMountIsNotScanned(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	root := t.TempDir()
	s.CheckDirectoryRoot = func(context.Context, string) error { return ErrDirectoryUnavailable }
	if e := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); !errors.Is(e, ErrDirectoryUnavailable) {
		t.Fatal(e)
	}
	p, e := s.DirectoryProgress()
	if e != nil || p.State != "waiting" || p.Pending != 1 {
		t.Fatal(p, e)
	}
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM scan_directory_snapshots`).Scan(&n)
	if n != 0 {
		t.Fatal("protected empty mount confirmed as a completed snapshot")
	}
}

func TestDirectoryQueueTruncatedExtensionsRequireMobiHeader(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	root := t.TempDir()
	header := make([]byte, 80)
	copy(header[60:], "BOOKMOBI")
	valid := writeBook(t, root, "truncated.az", header)
	bad := writeBook(t, root, "not-a-book.a", []byte("not a book"))
	if s.queueFormat(valid) != "azw3" || s.queueFormat(bad) != "" {
		t.Fatal("truncated extension detection")
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatal("source renamed", err)
	}
}

func TestDirectoryQueueDurableFileEventAndLease(t *testing.T) {
	db := testDB(t)
	s := directoryTestConnections(newTestService(db))
	root := t.TempDir()
	path := writeBook(t, root, "a.txt", []byte("old content"))
	if e := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); e != nil {
		t.Fatal(e)
	}
	before, _ := getScanFileByPath(db, path)
	directoryInfo, _ := os.Stat(root)
	os.WriteFile(path, []byte("changed content in place"), 0600)
	os.Chtimes(root, directoryInfo.ModTime(), directoryInfo.ModTime())
	if e := s.RememberDirectoryChanges([]string{path}, nil); e != nil {
		t.Fatal(e)
	}
	restarted := directoryTestConnections(newTestService(db))
	if e := restarted.ScanDirectoryQueue(context.Background(), root, false, 1, nil); e != nil {
		t.Fatal(e)
	}
	after, _ := getScanFileByPath(db, path)
	if before.Hash == after.Hash {
		t.Fatal("persisted in-place event lost")
	}
	var events int
	db.QueryRow(`SELECT count(*) FROM scan_directory_events`).Scan(&events)
	if events != 0 {
		t.Fatal("processed event not acknowledged", events)
	}
	if _, e := db.Exec(`INSERT INTO scan_directory_lease(id,owner,expires) VALUES(1,'another-process',?)`, time.Now().Unix()+60); e != nil {
		t.Fatal(e)
	}
	limited, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if e := s.ScanDirectoryQueue(limited, root, false, 1, nil); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("second scanner entered held lease")
	}
}

func TestDirectoryQueueStopGenerationPreservesTask(t *testing.T) {
	db := testDB(t)
	s := directoryTestConnections(newTestService(db))
	root := t.TempDir()
	writeBook(t, root, "a.txt", []byte("a"))
	writeBook(t, root, "b.txt", []byte("b"))
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.ScanDirectoryQueue(context.Background(), root, false, 1, func() error {
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
			time.Sleep(1200 * time.Millisecond)
			return nil
		})
	}()
	<-started
	if _, e := db.Exec(`UPDATE scan_directory_control SET stop_generation=stop_generation+1 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	p, e := s.DirectoryProgress()
	if e != nil || p.State != "paused" || p.Files != 1 || p.Pending == 0 {
		t.Fatal(p, e)
	}
	if e := directoryTestConnections(newTestService(db)).ScanDirectoryQueue(context.Background(), root, false, 1, nil); e != nil {
		t.Fatal(e)
	}
}

func TestDirectoryQueueImportProgressIsActualBooks(t *testing.T) {
	s := directoryTestConnections(newImportService(t, importTestDB(t), importLibraryDB(t)))
	root := t.TempDir()
	writeBook(t, root, "a.txt", []byte("different book a"))
	writeBook(t, root, "b.txt", []byte("different book b"))
	err := s.ScanDirectoryQueue(context.Background(), root, false, 1, func() error {
		rows, e := s.ReadyBatch(0, 100)
		if e != nil {
			return e
		}
		var hashes []string
		for _, r := range rows {
			hashes = append(hashes, r.Hash)
		}
		if len(hashes) == 0 {
			return nil
		}
		_, e = s.ImportContext(context.Background(), hashes, 1, false, ModeIndex)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.DirectoryProgress()
	if err != nil || p.Imported != 2 || p.Ready != 2 || !p.AutoImport || p.State != "done" {
		t.Fatal(p, err)
	}
}

func TestDirectoryQueueStreamsArchiveAfterOrdinaryFiles(t *testing.T) {
	s := directoryTestConnections(newImportService(t, importTestDB(t), importLibraryDB(t)))
	s.SupportedFormats["zip"] = true
	root := t.TempDir()
	normal := writeBook(t, root, "z.txt", []byte("ordinary book"))
	entries := []zipTestEntry{}
	for i := 0; i < 101; i++ {
		entries = append(entries, zipTestEntry{name: fmt.Sprintf("book-%03d.txt", i), body: []byte(fmt.Sprintf("unique book contents %d", i))})
	}
	writeZIPFixture(t, root, "a.zip", zipTestBytes(t, entries...))
	batches := 0
	err := s.ScanDirectoryQueue(context.Background(), root, false, 10, func() error {
		batches++
		rows, e := s.ReadyBatch(0, 200)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		if rows[0].Path != normal {
			r, e := getScanFileByPath(s.DB, normal)
			if e != nil || r == nil || r.Status != "indexed" {
				t.Fatal("archive ran before ordinary book", r, e)
			}
		}
		var hashes []string
		for _, r := range rows {
			hashes = append(hashes, r.Hash)
		}
		_, e = s.ImportContext(context.Background(), hashes, 1, false, ModeIndex)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.DirectoryProgress()
	if err != nil || p.State != "done" || p.Imported != 102 || p.Ready != 102 || batches < 3 {
		t.Fatal(p, batches, err)
	}
}

func TestDirectoryQueueArchiveFileScopeKeepsOtherReadyFiles(t *testing.T) {
	s := directoryTestConnections(newImportService(t, importTestDB(t), importLibraryDB(t)))
	s.SupportedFormats["zip"] = true
	otherRoot := t.TempDir()
	outside := writeBook(t, otherRoot, "outside.txt", []byte("do not import this"))
	if _, e := s.ScanAll(context.Background(), otherRoot, 10); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	archive := writeZIPFixture(t, root, "books.zip", zipTestBytes(t, zipTestEntry{name: "inside.txt", body: []byte("target archive book")}))
	e := s.ScanDirectoryQueue(context.Background(), archive, false, 1, func() error {
		rows, e := s.ReadyBatch(0, 100)
		if e != nil {
			return e
		}
		var hashes []string
		for _, r := range rows {
			if SourceWithin(r, archive) {
				hashes = append(hashes, r.Hash)
			}
		}
		if len(hashes) == 0 {
			return nil
		}
		_, e = s.ImportContext(context.Background(), hashes, 1, false, ModeIndex)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.DirectoryProgress()
	if e != nil || p.Imported != 1 || p.Mode != "incremental" {
		t.Fatal(p, e)
	}
	row, e := getScanFileByPath(s.DB, outside)
	if e != nil || row.Status != "ready" {
		t.Fatal("unrelated pending import modified", row, e)
	}
}

// A plain :memory: SQLite fixture is private to one connection. The lease
// heartbeat must share it instead of opening an unrelated empty database.
func directoryTestConnections(s *Service) *Service {
	s.DB.SetMaxOpenConns(1)
	if s.LibraryDB != nil {
		s.LibraryDB.SetMaxOpenConns(1)
	}
	return s
}

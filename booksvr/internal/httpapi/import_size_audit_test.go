package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/library"
	"testing"
	"time"
)

func TestImportSizeAuditRemovesOnlyActualSmallFilesAndResumes(t *testing.T) {
	w, db := newWriteFixture(t)
	_ = w
	users := scanTestDB(t)
	if e := library.EnsureImportSizeAudit(db); e != nil {
		t.Fatal(e)
	}
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	// Each candidate is seeded with a small recorded size. Actual size wins.
	for _, v := range []struct {
		id, book     int
		name, format string
		size         int
	}{{101, 100, "readme.txt", "TXT", 5}, {102, 101, "small.txt", "TXT", 5}, {103, 101, "valid.epub", "EPUB", 20480}, {104, 102, "stale.txt", "TXT", 20480}, {105, 103, "missing.txt", "TXT", -1}, {106, 104, "exact.txt", "TXT", 10240}, {107, 105, "shrank.txt", "TXT", 5}} {
		file := filepath.Join(root, v.name)
		if v.size >= 0 {
			if e := os.WriteFile(file, bytes.Repeat([]byte("a"), v.size), 0600); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := db.Exec(`INSERT OR IGNORE INTO books(id,title) VALUES(?,?)`, v.book, v.name); e != nil {
			t.Fatal(e)
		}
		recorded := v.size
		if recorded > 10240 {
			recorded = 1
		}
		if recorded < 0 {
			recorded = 5
		}
		if _, e := db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(?,?,?,?,?,0)`, v.id, v.book, v.format, file, recorded); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec(`INSERT INTO data(book,format,name) VALUES(?,?,?)`, v.book, v.format, v.name); e != nil {
			t.Fatal(e)
		}
		if _, e := users.Exec(`INSERT INTO scanfiles(name,path,hash,status,book_id) VALUES(?,?,?,'indexed',?)`, v.name, file, v.name, v.book); e != nil {
			t.Fatal(e)
		}
	}
	// The valid sibling should not be a candidate by recorded size.
	db.Exec(`UPDATE book_formats SET size=20480 WHERE id IN (103,107)`)
	opts := ServeOptions{ImportDir: importdir.Config{AllowedRoots: []string{root}}, LibraryRoot: t.TempDir()}
	if _, e := library.SyncSourceFiles(context.Background(), db, []string{root}, "", library.SourceSyncOptions{FullDiscovery: true}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		if e := runImportSizeAuditStep(context.Background(), opts, db, users, "generation1", 10240); e != nil {
			t.Fatal(i, e)
		}
	}
	status := importSizeAuditStatus(db)
	if status["state"] != "done" || status["removed_formats"] != int64(3) || status["removed_books"] != int64(2) || status["unreadable"] != int64(1) {
		t.Fatal(status)
	}
	for _, name := range []string{"readme.txt", "small.txt", "stale.txt", "exact.txt"} {
		if _, e := os.Stat(filepath.Join(root, name)); e != nil {
			t.Fatal("source removed", e)
		}
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM books WHERE id IN (101,102,103,104)`).Scan(&count)
	if count != 4 {
		t.Fatal("valid/unreadable books removed", count)
	}
	var statusName, code string
	if e := users.QueryRow(`SELECT status,json_extract(data,'$.skip_reason_code') FROM scanfiles WHERE name='readme.txt'`).Scan(&statusName, &code); e != nil || statusName != "drop" || code != "file.too_small" {
		t.Fatal(statusName, code, e)
	}
	// Re-entering the step after a restart preserves completed progress.
	if e := runImportSizeAuditStep(context.Background(), opts, db, users, "generation1", 10240); e != nil {
		t.Fatal(e)
	}
	if importSizeAuditStatus(db)["removed_formats"] != int64(3) {
		t.Fatal("replayed deletion")
	}
	if e := runImportSizeAuditStep(context.Background(), opts, db, users, "disabled", 0); e != nil {
		t.Fatal(e)
	}
	if importSizeAuditStatus(db)["state"] != "disabled" {
		t.Fatal("zero must disable cleanup")
	}
}
func TestImportSizeAuditProtectedMountAndFileRace(t *testing.T) {
	w, db := newWriteFixture(t)
	_ = w
	if e := library.EnsureImportSizeAudit(db); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	file := filepath.Join(root, "book.txt")
	os.WriteFile(file, []byte("small"), 0600)
	db.Exec(`INSERT INTO books(id,title) VALUES(110,'book')`)
	db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(110,110,'TXT',?,5,0)`, file)
	db.Exec(`INSERT INTO data(book,format,name) VALUES(110,'TXT','book')`)
	opts := ServeOptions{ImportDir: importdir.Config{AllowedRoots: []string{root}}}
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO source_sync_roots(root,identity,token) VALUES(?,'wrong-mount','bad-token')`, root); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		if e := runImportSizeAuditStep(context.Background(), opts, db, nil, "protected", 10240); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM books WHERE id=110`).Scan(&count)
	if count != 1 {
		t.Fatal("protected source removed")
	}
	db.Exec(`UPDATE import_size_audit SET generation='race',minimum=10240,state='running' WHERE id=1`)
	before, _ := os.Stat(file)
	replacement := filepath.Join(root, "replacement")
	os.WriteFile(replacement, []byte("other"), 0600)
	os.Remove(file)
	os.Rename(replacement, file)
	removed, e := library.PruneImportFormat(context.Background(), db, "race", 110, file, file, before, 10240, false)
	if e != nil || removed {
		t.Fatal("changed identity removed", removed, e)
	}
}

func TestImportSizeAuditQueuesWhileImportBusyAndContinuesAfterRestart(t *testing.T) {
	_, db := newWriteFixture(t)
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	file := filepath.Join(root, "readme.txt")
	os.WriteFile(file, []byte("small"), 0600)
	db.Exec(`INSERT INTO books(id,title) VALUES(210,'readme')`)
	db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(210,210,'TXT',?,5,0)`, file)
	db.Exec(`INSERT INTO data(book,format,name) VALUES(210,'TXT','readme')`)
	if _, e := library.SyncSourceFiles(context.Background(), db, []string{root}, "", library.SourceSyncOptions{FullDiscovery: true}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "auto.py")
	if e := SaveSettings(path, map[string]any{"import_allowed_roots": []string{root}, "import_min_file_size_kib": 10, "import_size_audit_requested": "busy-job"}); e != nil {
		t.Fatal(e)
	}
	var busy atomic.Bool
	busy.Store(true)
	opts := ServeOptions{SettingsPath: path, TryImportMaintenance: func(fn func() error) (bool, error) {
		if busy.Load() {
			return false, nil
		}
		return true, fn()
	}}
	_, stop, e := attachImportSizeAudit(context.Background(), opts, db, nil)
	if e != nil {
		t.Fatal(e)
	}
	wait := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(until) {
				t.Fatal(importSizeAuditStatus(db))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait(func() bool { return importSizeAuditStatus(db)["state"] == "running" })
	status := importSizeAuditStatus(db)
	if status["total"] != int64(1) || status["checked"] != int64(0) {
		t.Fatal(status)
	}
	stop() // restart the worker with the same persistent job
	busy.Store(false)
	_, stop, e = attachImportSizeAudit(context.Background(), opts, db, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	wait(func() bool { return importSizeAuditStatus(db)["state"] == "done" })
	if importSizeAuditStatus(db)["removed_books"] != int64(1) {
		t.Fatal(importSizeAuditStatus(db))
	}
	if _, e = os.Stat(file); e != nil {
		t.Fatal("source removed", e)
	}
}
func TestImportSizeAuditCandidateUsesPrimaryKeyCursor(t *testing.T) {
	db, _ := newTestDBFile(t)
	rows, e := db.Query("EXPLAIN QUERY PLAN "+importSizeCandidateQuery, 0, 100, 10240, 10240)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	details := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
			t.Fatal(e)
		}
		details += detail
	}
	if !strings.Contains(details, "INTEGER PRIMARY KEY") || strings.Contains(details, "TEMP B-TREE") {
		t.Fatal("candidate polling must not rescan and sort all small files", details)
	}
}

func TestImportSizeAuditImmediateUsesCheckpointAndKeepsProgress(t *testing.T) {
	_, db := newWriteFixture(t)
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	for i := 0; i < 6; i++ {
		file := filepath.Join(root, fmt.Sprintf("readme-%d.txt", i))
		os.WriteFile(file, []byte("small"), 0600)
		id := 300 + i
		db.Exec(`INSERT INTO books(id,title) VALUES(?,'readme')`, id)
		db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(?,?,'TXT',?,5,0)`, id, id, file)
		db.Exec(`INSERT INTO data(book,format,name) VALUES(?,'TXT','readme')`, id)
	}
	if _, e := library.SyncSourceFiles(context.Background(), db, []string{root}, "", library.SourceSyncOptions{FullDiscovery: true}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "auto.py")
	SaveSettings(path, map[string]any{"import_allowed_roots": []string{root}, "import_min_file_size_kib": 10, "import_size_audit_requested": "keep-this-job"})
	checkpoint := &importMaintenanceCheckpoint{}
	opts := ServeOptions{SettingsPath: path, ImportMaintenance: checkpoint, TryImportMaintenance: func(func() error) (bool, error) { return false, nil }}
	live, stop, e := attachImportSizeAudit(context.Background(), opts, db, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	if e = live.RequestImportSizeAudit(); e != nil {
		t.Fatal(e)
	}
	var gen string
	db.QueryRow(`SELECT generation FROM import_size_audit WHERE id=1`).Scan(&gen)
	if gen != "keep-this-job" {
		t.Fatal("active request restarted", gen)
	}
	until := time.Now().Add(3 * time.Second)
	for importSizeAuditStatus(db)["checked"].(int64) == 0 {
		checkpoint.Run(context.Background())
		if time.Now().After(until) {
			t.Fatal(importSizeAuditStatus(db))
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := importSizeAuditStatus(db)
	checked := status["checked"].(int64)
	if checked <= 0 || checked > 4 || status["immediate_requested"] != true {
		t.Fatal(status)
	}
	if e = live.RequestImportSizeAudit(); e != nil {
		t.Fatal(e)
	}
	if importSizeAuditStatus(db)["checked"] != checked {
		t.Fatal("repeated click reset progress")
	}
	until = time.Now().Add(3 * time.Second)
	for importSizeAuditStatus(db)["state"] != "done" {
		checkpoint.Run(context.Background())
		if time.Now().After(until) {
			t.Fatal(importSizeAuditStatus(db))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if importSizeAuditStatus(db)["state"] != "done" {
		t.Fatal(importSizeAuditStatus(db))
	}
	SaveSettings(path, map[string]any{"import_min_file_size_kib": 0})
	if e = live.RequestImportSizeAudit(); e == nil {
		t.Fatal("zero threshold must not start")
	}
	for _, spec := range ImportRouteSpecs(ImportConfig{RunSizeAudit: live.RequestImportSizeAudit}) {
		if spec.Pattern == "/api/admin/import/size-audit/run" {
			if !spec.Auth || !spec.Admin {
				t.Fatal("unsafe audit permissions")
			}
			return
		}
	}
	t.Fatal("manual route missing")
}

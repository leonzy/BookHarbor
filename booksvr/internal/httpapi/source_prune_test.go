package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/library"
)

func TestSourcePruneOutboxOnlyCompletedExactSources(t *testing.T) {
	_, books := newWriteFixture(t)
	if e := library.Migrate(books); e != nil {
		t.Fatal(e)
	}
	users, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer users.Close()
	if _, e = users.Exec(`CREATE TABLE scanfiles(id INTEGER PRIMARY KEY,book_id INTEGER,status TEXT,path TEXT,data TEXT); INSERT INTO scanfiles VALUES(1,123,'indexed','old','{}'),(2,123,'indexed','other','{}'),(3,123,'ready','old','{}'),(4,123,'indexed','virtual','{"source_path":"old"}')`); e != nil {
		t.Fatal(e)
	}
	if _, e = books.Exec(`INSERT INTO source_prune_removed(format_id,book_id,path,format,removed_book) VALUES(1,123,'old','TXT',0)`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = flushSourcePruneRemovals(books, users); e != nil {
			t.Fatal(e)
		}
	}
	for id, want := range map[int]string{1: "drop", 2: "indexed", 3: "ready", 4: "drop"} {
		var got string
		if e = users.QueryRow(`SELECT status FROM scanfiles WHERE id=?`, id).Scan(&got); e != nil || got != want {
			t.Fatal(id, got, e)
		}
	}
}

func TestSourcePruneOutboxBatchRollback(t *testing.T) {
	_, books := newWriteFixture(t)
	if e := library.Migrate(books); e != nil {
		t.Fatal(e)
	}
	users, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer users.Close()
	if _, e = users.Exec(`CREATE TABLE scanfiles(id INTEGER PRIMARY KEY,book_id INTEGER,status TEXT,path TEXT,data TEXT); CREATE TABLE items(book_id INTEGER); CREATE TABLE book_aliases(book_id INTEGER); INSERT INTO items VALUES(1),(2); INSERT INTO book_aliases VALUES(1),(2); INSERT INTO scanfiles VALUES(1,1,'indexed','one','{}'),(2,2,'indexed','two','{}'); CREATE TRIGGER fail_second BEFORE UPDATE ON scanfiles WHEN OLD.id=2 BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = books.Exec(`INSERT INTO source_prune_removed(format_id,book_id,path,format,removed_book) VALUES(1,1,'one','TXT',1),(2,2,'two','TXT',1)`); e != nil {
		t.Fatal(e)
	}
	if e = flushSourcePruneRemovals(books, users); e == nil {
		t.Fatal("expected fixture failure")
	}
	var count int
	users.QueryRow(`SELECT count(*) FROM scanfiles WHERE status='indexed'`).Scan(&count)
	if count != 2 {
		t.Fatal("partial user transaction", count)
	}
	users.QueryRow(`SELECT count(*) FROM items`).Scan(&count)
	if count != 2 {
		t.Fatal("partial item cleanup", count)
	}
	books.QueryRow(`SELECT count(*) FROM source_prune_removed WHERE applied=0`).Scan(&count)
	if count != 2 {
		t.Fatal("acknowledged failed batch", count)
	}
	users.Exec(`DROP TRIGGER fail_second`)
	if e = flushSourcePruneRemovals(books, users); e != nil {
		t.Fatal(e)
	}
	books.QueryRow(`SELECT count(*) FROM source_prune_removed WHERE applied=0`).Scan(&count)
	if count != 0 {
		t.Fatal("outbox not replayed", count)
	}
	users.QueryRow(`SELECT (SELECT count(*) FROM items)+(SELECT count(*) FROM book_aliases)`).Scan(&count)
	if count != 0 {
		t.Fatal("catalog associations retained", count)
	}
}

func TestSourcePruneWorkerWaitsForReconciliation(t *testing.T) {
	_, db := newWriteFixture(t)
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	// Only this test's completed import table is needed by the outbox.
	if _, e := db.Exec(`CREATE TABLE IF NOT EXISTS scanfiles(id INTEGER PRIMARY KEY,book_id INTEGER,status TEXT,path TEXT,data TEXT)`); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	path := filepath.Join(root, "gone.txt")
	os.WriteFile(path, []byte("book"), 0600)
	// The legacy Windows fallback identity uses creation time, not an inode.
	// Keep fixture identities distinct; production Linux uses inode identity.
	if runtime.GOOS == "windows" {
		time.Sleep(20 * time.Millisecond)
	}
	os.WriteFile(filepath.Join(root, "live.txt"), []byte("live"), 0600)
	db.Exec(`INSERT INTO books(id,title) VALUES(555,'Keep title')`)
	if e := library.RegisterFormat(db, 555, "TXT", path, 4, 0); e != nil {
		t.Fatal(e)
	}
	run := func() {
		if _, e := library.SyncSourceFiles(context.Background(), db, []string{root}, "", library.SourceSyncOptions{FullDiscovery: true}); e != nil {
			t.Fatal(e)
		}
	}
	run()
	os.Remove(path)
	run()
	db.Exec(`UPDATE source_prune_pending SET seen_at=0`)
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	stop := startSourcePruneWorker(ctx, ServeOptions{Out: os.Stderr, ImportDir: importdir.Config{AllowedRoots: []string{root}}}, db, db, ready.Load)
	defer func() { cancel(); stop() }()
	time.Sleep(250 * time.Millisecond)
	var count int
	db.QueryRow(`SELECT count(*) FROM books WHERE id=555`).Scan(&count)
	if count != 1 {
		t.Fatal("pruned during baseline")
	}
	ready.Store(true)
	until := time.Now().Add(4 * time.Second)
	for {
		if e := db.QueryRow(`SELECT count(*) FROM books WHERE id=555`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count == 0 {
			break
		}
		if time.Now().After(until) {
			var pending, missing int
			db.QueryRow(`SELECT count(*) FROM source_prune_pending`).Scan(&pending)
			db.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE missing=1`).Scan(&missing)
			t.Fatalf("worker did not prune; pending=%d missing=%d", pending, missing)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSourcePrunePrivateProtocolRejectsInvalidRequests(t *testing.T) {
	for _, input := range []string{`{}`, `{"db":"x","users":"y","roots":["/books"],"limit":501}`, `{"unknown":1}`} {
		if e := RunSourcePruneWorker(context.Background(), strings.NewReader(input), &bytes.Buffer{}); e == nil {
			t.Fatal(input)
		}
	}
}

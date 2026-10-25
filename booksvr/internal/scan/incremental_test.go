package scan

import (
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/userdb"
	"testing"
)

func TestScanDuplicateTemporaryHashIsDone(t *testing.T) {
	db := testDB(t)
	s := newTestService(db)
	root := t.TempDir()
	writeBook(t, root, "a.txt", []byte("same content"))
	b := writeBook(t, root, "b.txt", []byte("same content"))
	if _, e := s.ScanAll(context.Background(), root, 500); e != nil {
		t.Fatal(e)
	}
	row, e := getScanFileByPath(db, b)
	if e != nil || row == nil || row.Status != userdb.ScanStatusDrop {
		t.Fatal(row, e)
	}
	r, e := s.ScanAll(context.Background(), root, 500)
	if e != nil || r.Inserted != 0 || s.Progress().Processed != 0 {
		t.Fatal("duplicate processed again", r, e)
	}
	done, e := DonePaths(db, b)
	if e != nil || !done[b] {
		t.Fatal("exact file missing from done", done, e)
	}
}
func TestScanIncrementalChangedFileOnly(t *testing.T) {
	db := testDB(t)
	s := newTestService(db)
	root := t.TempDir()
	p := writeBook(t, root, "a.txt", []byte("first"))
	other := writeBook(t, root, "other.txt", []byte("other"))
	s.ScanAll(context.Background(), root, 500)
	before, _ := getScanFileByPath(db, other)
	os.WriteFile(p, []byte("new content different size"), 0600)
	r, e := s.ScanAll(WithSourceChanges(context.Background()), p, 500)
	if e != nil || r.Inserted != 1 || s.Progress().Processed != 1 {
		t.Fatal(r, e)
	}
	after, _ := getScanFileByPath(db, other)
	if before.Hash != after.Hash || before.ScanID != after.ScanID {
		t.Fatal("unrelated row modified")
	}
	r, e = s.ScanAll(WithSourceChanges(context.Background()), p, 500)
	if e != nil || r.Inserted != 0 {
		t.Fatal("unchanged event reprocessed", r, e)
	}
	if filepath.Base(p) != "a.txt" {
		t.Fatal(p)
	}
}

func TestScanIncrementalDoesNotReimportIndexedRestore(t *testing.T) {
	db := testDB(t)
	s := newTestService(db)
	root := t.TempDir()
	p := writeBook(t, root, "original.txt", []byte("old content"))
	s.ScanAll(context.Background(), root, 500)
	if _, e := db.Exec(`UPDATE scanfiles SET status='indexed',book_id=77 WHERE path=?`, p); e != nil {
		t.Fatal(e)
	}
	os.Remove(p)
	os.WriteFile(p, []byte("restored content"), 0600)
	r, e := s.ScanAll(WithSourceChanges(context.Background()), p, 500)
	if e != nil || r.Inserted != 0 || s.Progress().Processed != 0 {
		t.Fatal("indexed record reset on restore", r, e)
	}
	row, _ := getScanFileByPath(db, p)
	if row.Status != "indexed" || row.BookID != 77 {
		t.Fatal(row)
	}
}

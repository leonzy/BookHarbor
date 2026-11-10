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

func TestScanRestoresPrunedSourceWithUnchangedSignature(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "incremental"}[incremental], func(t *testing.T) {
			db := testDB(t)
			s := newTestService(db)
			root := t.TempDir()
			path := writeBook(t, root, "restore.txt", []byte("restored text"))
			if _, e := s.ScanAll(context.Background(), root, 500); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec(`UPDATE scanfiles SET status='drop',book_id=0,data=json_set(data,'$.skip_reason_code','source.missing','$.skip_reason','gone') WHERE path=?`, path); e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			if incremental {
				ctx = WithSourceChanges(ctx)
			}
			r, e := s.ScanAll(ctx, path, 500)
			if e != nil || r.Inserted != 1 {
				t.Fatal(r, e)
			}
			row, e := getScanFileByPath(db, path)
			if e != nil || row.Data["skip_reason_code"] == "source.missing" {
				t.Fatal(row, e)
			}
			r, e = s.ScanAll(ctx, path, 500)
			if e != nil || r.Inserted != 0 {
				t.Fatal("repeated unchanged import", r, e)
			}
		})
	}
}

func TestScanRestoresPrunedZIPMember(t *testing.T) {
	root := t.TempDir()
	s := newImportService(t, importTestDB(t), importLibraryDB(t))
	s.SupportedFormats["zip"] = true
	archive := writeZIPFixture(t, root, "restore.zip", zipTestBytes(t, zipTestEntry{name: "book.txt", body: []byte("第一章\n这是一段正文。\n")}))
	r, e := s.ScanAll(context.Background(), root, 500)
	if e != nil || r.Ready != 1 {
		t.Fatal(r, e)
	}
	if _, e = s.DB.Exec(`UPDATE scanfiles SET status='drop',book_id=0,data=json_set(data,'$.skip_reason_code','source.missing') WHERE path LIKE ?`, archive+"!/%"); e != nil {
		t.Fatal(e)
	}
	r, e = s.ScanAll(WithSourceChanges(context.Background()), archive, 500)
	if e != nil || r.Ready != 1 {
		t.Fatal(r, e)
	}
	r, e = s.ScanAll(context.Background(), root, 500)
	if e != nil || r.Ready != 0 {
		t.Fatal("repeated ZIP scan", r, e)
	}
}

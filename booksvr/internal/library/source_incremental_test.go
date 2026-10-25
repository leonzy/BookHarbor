package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/sourcefile"
	"testing"
)

func TestSourceIncrementalScopeMoveDeleteRestore(t *testing.T) {
	db, root, run := sourceFixture(t)
	old := filepath.Join(root, "old", "book.txt")
	other := filepath.Join(root, "unrelated.txt")
	sourceWrite(t, old)
	sourceWrite(t, other)
	sourceRegister(t, db, 1, old, "TXT")
	sourceRegister(t, db, 2, other, "TXT")
	run()
	next := filepath.Join(root, "renamed")
	sourceMove(t, filepath.Dir(old), next)
	os.Remove(other)
	sync := func(paths ...string) SourceSyncResult {
		r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: paths})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := sync(filepath.Dir(old), next)
	if r.Checked != 1 || r.Moved != 1 || r.Missing != 0 || r.Inspected > 3 {
		t.Fatalf("scope: %+v", r)
	}
	sourceMissing(t, db, 2, false)
	path := filepath.Join(next, "book.txt")
	os.Remove(path)
	r = sync(path)
	if r.Checked != 1 || r.Missing != 1 {
		t.Fatal(r)
	}
	sourceWrite(t, path)
	r = sync(path)
	if r.Restored != 1 {
		t.Fatal(r)
	}
	// Formats inserted after the baseline must enter the physical path index.
	fresh := filepath.Join(root, "fresh.txt")
	sourceWrite(t, fresh)
	sourceRegister(t, db, 3, fresh, "TXT")
	sync(fresh)
	os.Remove(fresh)
	if sync(fresh).Missing != 1 {
		t.Fatal("new format lost")
	}
}
func TestSourceIncrementalIdleAndProtectedAvoidBookQuery(t *testing.T) {
	db, root, run := sourceFixture(t)
	p := filepath.Join(root, "book.txt")
	sourceWrite(t, p)
	sourceRegister(t, db, 1, p, "TXT")
	run()
	// Deliberately remove the table: a health-only tick must not read it.
	if _, e := db.Exec(`DROP TABLE book_formats`); e != nil {
		t.Fatal(e)
	}
	r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true})
	if e != nil || r.Checked != 0 || r.Inspected != 0 {
		t.Fatal(r, e)
	}
	os.Remove(filepath.Join(root, ".talebook-source-health"))
	r, e = SyncSourceFiles(context.Background(), db, []string{root}, "")
	if e != nil || r.Roots[root] == "ok" {
		t.Fatal(r, e)
	}
}
func TestSourceIncrementalArchivePhysicalPath(t *testing.T) {
	db, root, run := sourceFixture(t)
	archive := filepath.Join(root, "archive.zip")
	sourceWrite(t, archive)
	fp, _ := sourcefile.Fingerprint(archive)
	ref := sourcefile.Reference{Archive: archive, Fingerprint: fp, Chain: []string{"book.txt"}, Hash: "sha256:" + strings.Repeat("a", 64), Size: 4}
	sourceRegister(t, db, 1, sourcefile.Encode(ref), "TXT")
	run()
	next := filepath.Join(root, "renamed.zip")
	sourceMove(t, archive, next)
	r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{archive, next}})
	if e != nil || r.Checked != 1 || r.Moved != 1 {
		t.Fatal(r, e)
	}
	var path string
	db.QueryRow(`SELECT path FROM book_formats WHERE book_id=1`).Scan(&path)
	got, e := sourcefile.Decode(path)
	if e != nil || got.Archive != next {
		t.Fatal(got, e)
	}
}
func TestSourceIncrementalQueryUsesPhysicalIndex(t *testing.T) {
	db, _, _ := sourceFixture(t)
	rows, e := db.Query(`EXPLAIN QUERY PLAN SELECT format_id FROM source_sync_paths WHERE physical_path>=? AND physical_path<?`, "/books/a/", "/books/a0")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var a, b, c int
		var detail string
		rows.Scan(&a, &b, &c, &detail)
		if strings.Contains(detail, "source_sync_physical_path") && strings.Contains(detail, "SEARCH") {
			found = true
		}
	}
	if !found {
		t.Fatal("incremental lookup does not use physical path index")
	}
}

func TestSourceIncrementalSplitMoveEvents(t *testing.T) {
	db, root, run := sourceFixture(t)
	old := filepath.Join(root, "old.txt")
	sourceWrite(t, old)
	sourceRegister(t, db, 1, old, "TXT")
	run()
	next := filepath.Join(root, "next.txt")
	sourceMove(t, old, next)
	r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{old}})
	if e != nil || r.Missing != 1 {
		t.Fatal(r, e)
	}
	r, e = SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{next}})
	if e != nil || r.Moved != 1 || r.Restored != 1 {
		t.Fatal(r, e)
	}
}

func TestSourceIncrementalLargeCatalogStaysScoped(t *testing.T) {
	db, root, run := sourceFixture(t)
	p := filepath.Join(root, "book.txt")
	sourceWrite(t, p)
	sourceRegister(t, db, 1, p, "TXT")
	run()
	// Synthetic unrelated rows make accidental all-catalog reconciliation fail.
	if _, e := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(10) UNION ALL SELECT i+1 FROM n WHERE i<10009) INSERT INTO books(id,title,path) SELECT i,'Unrelated','unrelated' FROM n`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) SELECT id,'TXT',?||id||'.txt',4,0 FROM books WHERE id>=10`, filepath.Join(root, "unrelated")+string(filepath.Separator)); e != nil {
		t.Fatal(e)
	}
	r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{p}})
	if e != nil || r.Checked != 1 || r.Missing != 0 || r.Inspected != 1 {
		t.Fatal("unrelated catalog loaded", r, e)
	}
}

func TestSourceNewArchiveCanMoveImmediatelyAfterImport(t *testing.T) {
	db, root, run := sourceFixture(t)
	run()
	archive := filepath.Join(root, "new.zip")
	sourceWrite(t, archive)
	fp, _ := sourcefile.Fingerprint(archive)
	ref := sourcefile.Reference{Archive: archive, Fingerprint: fp, Chain: []string{"book.txt"}, Hash: "sha256:" + strings.Repeat("a", 64), Size: 999}
	if _, e := db.Exec(`INSERT INTO books(id,title,path) VALUES(1,'New book',?)`, root); e != nil {
		t.Fatal(e)
	}
	if e := RegisterFormat(db, 1, "TXT", sourcefile.Encode(ref), 999, 0); e != nil {
		t.Fatal(e)
	}
	next := filepath.Join(root, "renamed.zip")
	sourceMove(t, archive, next)
	r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{archive, next}})
	if e != nil || r.Moved != 1 || r.Missing != 0 {
		t.Fatal("immediate archive rename failed", r, e)
	}
}

func TestSourceProtectedEventsDoNotLoadOtherRoots(t *testing.T) {
	db, root, run := sourceFixture(t)
	sourceWrite(t, filepath.Join(root, "book.txt"))
	run()
	other := t.TempDir()
	sourceWrite(t, filepath.Join(other, "book.txt"))
	if _, e := SyncSourceFiles(context.Background(), db, []string{root, other}, ""); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(root, ".talebook-source-health"))
	db.Exec(`DROP TABLE book_formats`)
	r, e := SyncSourceFiles(context.Background(), db, []string{root, other}, "", SourceSyncOptions{Incremental: true, ChangedPaths: []string{root}})
	if e != nil || r.Roots[other] != "ok" || r.Roots[root] == "ok" || r.Checked != 0 {
		t.Fatal(r, e)
	}
}

func TestSourceIncrementalOuterQueryUsesPrimaryKeys(t *testing.T) {
	db, _, _ := sourceFixture(t)
	rows, e := db.Query(`EXPLAIN QUERY PLAN `+sourceTrackedSelect+` WHERE f.id IN (SELECT format_id FROM source_sync_paths WHERE physical_path=? UNION ALL SELECT format_id FROM source_sync_paths WHERE physical_path>=? AND physical_path<?)`, "/books/a.txt", "/books/a.txt/", "/books/a.txt0")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	formats, physical := false, false
	for rows.Next() {
		var a, b, c int
		var detail string
		rows.Scan(&a, &b, &c, &detail)
		if strings.Contains(detail, "SEARCH f USING INTEGER PRIMARY KEY") {
			formats = true
		}
		if strings.Contains(detail, "source_sync_physical_path") {
			physical = true
		}
		if strings.Contains(detail, "SCAN f") || strings.Contains(detail, "SCAN s") {
			t.Fatal("full table scan", detail)
		}
	}
	if !formats || !physical {
		t.Fatal("index plan missing", formats, physical)
	}
}

func TestSourceFormatDeleteCleansStateWithoutForeignKeys(t *testing.T) {
	db, root, _ := sourceFixture(t)
	p := filepath.Join(root, "book.txt")
	sourceWrite(t, p)
	db.Exec(`INSERT INTO books(id,title,path) VALUES(1,'Book',?)`, root)
	if e := RegisterFormat(db, 1, "TXT", p, 4, 0); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`DELETE FROM book_formats`); e != nil {
		t.Fatal(e)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM source_sync_formats`).Scan(&n)
	if n != 0 {
		t.Fatal("orphaned state", n)
	}
	// Also tolerate pre-existing orphan states from older non-FK connections.
	db.Exec(`INSERT INTO source_sync_formats VALUES(1,'old',999,0,1)`)
	if e := RegisterFormat(db, 1, "TXT", p, 4, 0); e != nil {
		t.Fatal(e)
	}
	var size int64
	var missing int
	db.QueryRow(`SELECT size,missing FROM source_sync_formats WHERE format_id=1`).Scan(&size, &missing)
	if size != 4 || missing != 0 {
		t.Fatal(size, missing)
	}
}

func TestSourceIncrementalCopyThenUnlinkAcrossBatches(t *testing.T) {
	db, root, run := sourceFixture(t)
	old := filepath.Join(root, "original.txt")
	next := filepath.Join(root, "copied.txt")
	sourceWrite(t, old)
	sourceRegister(t, db, 1, old, "TXT")
	h, _ := sourceHash(context.Background(), old)
	SeedSourceHashes(db, map[string]string{old: h})
	run()
	sync := func(p string) SourceSyncResult {
		r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{p}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	sourceWrite(t, next)
	if r := sync(next); r.Moved != 0 {
		t.Fatal(r)
	}
	var candidates int
	db.QueryRow(`SELECT count(*) FROM source_sync_candidates WHERE path=?`, next).Scan(&candidates)
	if candidates != 1 {
		t.Fatal("copy not indexed as a candidate")
	}
	os.Remove(old)
	if r := sync(old); r.Moved != 1 || r.Missing != 0 {
		t.Fatal("split copy/unlink lost", r)
	}
	sourceMissing(t, db, 1, false)
	var path string
	db.QueryRow(`SELECT path FROM book_formats WHERE book_id=1`).Scan(&path)
	if path != next {
		t.Fatal(path)
	}
	db.QueryRow(`SELECT count(*) FROM source_sync_candidates WHERE path=?`, next).Scan(&candidates)
	if candidates != 0 {
		t.Fatal("claimed candidate retained")
	}
}
func TestSourceIncrementalReadonlyPartialSnapshotAndEmptyGuard(t *testing.T) {
	for _, other := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-file", true: "other-live-file"}[other], func(t *testing.T) {
			db, root, run := sourceFixture(t)
			p := filepath.Join(root, "gone.txt")
			sourceWrite(t, p)
			sourceRegister(t, db, 1, p, "TXT")
			if other {
				q := filepath.Join(root, "still-here.txt")
				sourceWrite(t, q)
				sourceRegister(t, db, 2, q, "TXT")
			}
			run()
			db.Exec(`UPDATE source_sync_roots SET token=''`)
			os.Remove(filepath.Join(root, ".talebook-source-health"))
			os.Remove(p)
			r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{p}})
			if e != nil {
				t.Fatal(e)
			}
			if other {
				if r.Missing != 1 || r.Roots[root] != "ok" {
					t.Fatal(r)
				}
			} else {
				if r.Missing != 0 || r.Roots[root] == "ok" {
					t.Fatal("empty root unprotected", r)
				}
			}
		})
	}
}
func TestSourceCandidateLookupUsesSizeIndex(t *testing.T) {
	db, _, _ := sourceFixture(t)
	rows, e := db.Query(`EXPLAIN QUERY PLAN SELECT path FROM source_sync_candidates WHERE size=?`, 4)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var a, b, c int
		var detail string
		rows.Scan(&a, &b, &c, &detail)
		if strings.Contains(detail, "SEARCH") && strings.Contains(detail, "source_sync_candidate_size") {
			found = true
		}
	}
	if !found {
		t.Fatal("candidate size index not used")
	}
}

func TestSourceCandidateAmbiguityResolvesAfterCopyDeleted(t *testing.T) {
	db, root, run := sourceFixture(t)
	old := filepath.Join(root, "old.txt")
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	sourceWrite(t, old)
	sourceRegister(t, db, 1, old, "TXT")
	h, _ := sourceHash(context.Background(), old)
	SeedSourceHashes(db, map[string]string{old: h})
	run()
	sync := func(paths ...string) SourceSyncResult {
		r, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: paths})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	sourceWrite(t, a)
	sourceWrite(t, b)
	sync(a, b)
	os.Remove(old)
	if r := sync(old); r.Moved != 0 || r.Missing != 1 {
		t.Fatal("ambiguous copy associated", r)
	}
	os.Remove(b)
	if r := sync(b); r.Moved != 1 || r.Restored != 1 {
		t.Fatal("resolved copy not recovered", r)
	}
	var path string
	db.QueryRow(`SELECT path FROM book_formats WHERE book_id=1`).Scan(&path)
	if path != a {
		t.Fatal(path)
	}
}

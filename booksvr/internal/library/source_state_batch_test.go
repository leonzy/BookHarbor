package library

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

func stateBatchFixture(t *testing.T, count int) (*sql.DB, []sourceStateUpdate) {
	t.Helper()
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var updates []sourceStateUpdate
	for id := 1; id <= count; id++ {
		path := fmt.Sprintf("/source/%d.txt", id)
		sourceRegister(t, db, id, path, "TXT")
		if _, err := db.Exec(`INSERT INTO source_sync_formats VALUES(?, '2080:123',4,1,0)`, id); err != nil {
			t.Fatal(err)
		}
		updates = append(updates, sourceStateUpdate{id: int64(id), path: path, file: sourceDiskFile{key: "2096:123", size: 4, mtime: 1}, statOnly: true})
	}
	return db, updates
}

func TestSourceStateBatchStatOnlyAvoidsAvailabilityTriggers(t *testing.T) {
	db, updates := stateBatchFixture(t, sourceStateBatchSize*2+7)
	// This checks the SQL column-level contract, including across batch edges.
	if _, err := db.Exec(`CREATE TRIGGER reject_stat_availability BEFORE UPDATE OF missing,format_id ON source_sync_formats BEGIN SELECT RAISE(ABORT,'unexpected availability update'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	missing, restored, err := applySourceStates(context.Background(), tx, updates)
	if err != nil || missing != 0 || restored != 0 {
		t.Fatal(missing, restored, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE identity='2096:123' AND missing=0`).Scan(&n); err != nil || n != len(updates) {
		t.Fatal(n, err)
	}
}

func TestSourceStateBatchCountsAndConcurrentPathGuards(t *testing.T) {
	db, updates := stateBatchFixture(t, 8)
	for _, id := range []int{2, 5, 6, 8} {
		if _, err := db.Exec(`UPDATE source_sync_formats SET missing=1 WHERE format_id=?`, id); err != nil {
			t.Fatal(err)
		}
		updates[id-1].previousMissing = 1
	}
	updates[0].missing, updates[0].statOnly = 1, false
	updates[1].statOnly = false
	updates[2].statOnly = false
	updates[3].path, updates[3].missing, updates[3].statOnly = "/stale-path.txt", 1, false
	updates[4].statOnly = false
	updates[5].missing = 1
	updates[7].missing = 1
	for _, q := range []string{
		`DELETE FROM source_sync_formats WHERE format_id IN (3,7)`,
		`DELETE FROM book_formats WHERE id=5`,
		`UPDATE book_formats SET path='/new-path.txt' WHERE id=8`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	missing, restored, err := applySourceStates(context.Background(), tx, updates)
	if err != nil || missing != 1 || restored != 1 {
		t.Fatal(missing, restored, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		id, missing int
		identity    string
	}{
		{1, 1, "2096:123"}, {2, 0, "2096:123"}, {3, 0, "2096:123"},
		{4, 0, "2080:123"}, {6, 1, "2096:123"}, {7, 0, "2096:123"}, {8, 1, "2080:123"},
	} {
		var identity string
		var missing int
		if err := db.QueryRow(`SELECT identity,missing FROM source_sync_formats WHERE format_id=?`, want.id).Scan(&identity, &missing); err != nil || identity != want.identity || missing != want.missing {
			t.Fatal(want, identity, missing, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE format_id=5`).Scan(&n); err != nil || n != 0 {
		t.Fatal("deleted format resurrected", n, err)
	}
}

func TestSourceStateBatchRollsBackEarlierBatches(t *testing.T) {
	db, updates := stateBatchFixture(t, sourceStateBatchSize+5)
	for i := range updates {
		updates[i].missing, updates[i].statOnly = 1, false
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_state_batch BEFORE UPDATE OF missing ON source_sync_formats WHEN NEW.format_id=%d BEGIN SELECT RAISE(ABORT,'second batch rejected'); END`, sourceStateBatchSize+1)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, _, err = applySourceStates(context.Background(), tx, updates)
	if err == nil || !strings.Contains(err.Error(), "second batch rejected") {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE missing=1`).Scan(&n); err != nil || n != sourceStateBatchSize {
		t.Fatal("first batch was not exercised", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE missing=1`).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial state committed", n, err)
	}
}

func TestSourceStateBatchMaintainsBrowseProjections(t *testing.T) {
	db := newQueryDB(t)
	for _, q := range []string{
		`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(11,1,'EPUB','/one.epub',1000,1),(12,2,'PDF','/two.pdf',2000,1),(13,3,'TXT','/three.txt',500,1)`,
		`INSERT INTO data(book,format,name,uncompressed_size) VALUES(2,'PDF','two',2000),(3,'TXT','three',500)`,
		`INSERT INTO source_sync_formats SELECT id,'2080:123',size,1,0 FROM book_formats`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	updates := []sourceStateUpdate{
		{id: 11, path: "/one.epub", file: sourceDiskFile{key: "2096:123", size: 1000, mtime: 1}, missing: 1},
		{id: 12, path: "/two.pdf", file: sourceDiskFile{key: "2096:124", size: 2000, mtime: 1}, missing: 1},
		{id: 13, path: "/three.txt", file: sourceDiskFile{key: "2096:125", size: 500, mtime: 1}, statOnly: true},
	}
	apply := func(wantMissing, wantRestored int) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		missing, restored, err := applySourceStates(context.Background(), tx, updates)
		if err != nil || missing != wantMissing || restored != wantRestored {
			t.Fatal(missing, restored, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	apply(2, 0)
	for _, format := range []string{"EPUB", "PDF"} {
		_, total, err := BrowseBooks(context.Background(), db, BrowseQuery{Format: format, Size: 20})
		if err != nil || total != 0 {
			t.Fatal(format, total, err)
		}
	}
	sourceMissing(t, db, 1, true)
	sourceMissing(t, db, 2, true)
	sourceMissing(t, db, 3, false)
	var size int
	if err := db.QueryRow(`SELECT size FROM book_sort_sizes WHERE book_id=2`).Scan(&size); err != nil || size != 0 {
		t.Fatal("missing source still ranked by size", size, err)
	}
	for i := 0; i < 2; i++ {
		updates[i].missing, updates[i].previousMissing = 0, 1
	}
	apply(0, 2)
	for _, format := range []string{"EPUB", "PDF", "TXT"} {
		_, total, err := BrowseBooks(context.Background(), db, BrowseQuery{Format: format, Size: 20})
		if err != nil || total != 1 {
			t.Fatal(format, total, err)
		}
	}
	if err := db.QueryRow(`SELECT size FROM book_sort_sizes WHERE book_id=2`).Scan(&size); err != nil || size != 2000 {
		t.Fatal("size ranking not restored", size, err)
	}
}

var registerSourceMarkerLoss sync.Once
var registerSourceMarkerLossErr error

func TestSourceSyncMarkerLostDuringStateWriteRollsBack(t *testing.T) {
	registerSourceMarkerLoss.Do(func() {
		registerSourceMarkerLossErr = sqlite.RegisterScalarFunction("test_source_remove_health_marker", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return int64(1), os.Remove(args[0].(string))
		})
	})
	if registerSourceMarkerLossErr != nil {
		t.Fatal(registerSourceMarkerLossErr)
	}
	db, root, run := sourceFixture(t)
	path := filepath.Join(root, "book.txt")
	sourceWrite(t, path)
	sourceRegister(t, db, 1, path, "TXT")
	run()
	marker := strings.ReplaceAll(filepath.Join(root, ".talebook-source-health"), "'", "''")
	q := `CREATE TRIGGER lose_source_marker AFTER UPDATE OF missing ON source_sync_formats WHEN NEW.missing=1 BEGIN SELECT test_source_remove_health_marker('` + marker + `'); END`
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	result, err := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{Incremental: true, FullDiscovery: true, ChangedPaths: []string{path}})
	if err == nil || result.Missing != 0 || result.Restored != 0 || result.Roots[root] == "ok" {
		t.Fatal(result, err)
	}
	var missing int
	if err := db.QueryRow(`SELECT missing FROM source_sync_formats WHERE format_id=1`).Scan(&missing); err != nil || missing != 0 {
		t.Fatal("offline root committed a missing state", missing, err)
	}
}

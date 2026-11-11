package coverart

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func retryFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,path TEXT,has_cover INTEGER);INSERT INTO books VALUES(1,'',0)`)
	if e = MigrateQueue(db); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "book.epub")
	os.WriteFile(path, []byte("invalid zip"), 0600)
	if e = Queue(db, 1, path, "EPUB"); e != nil {
		t.Fatal(e)
	}
	return db, path
}

func TestMalformedCoverStopsAndChangedSourceRearms(t *testing.T) {
	db, path := retryFixture(t)
	worked, e := WorkOne(context.Background(), db, t.TempDir())
	if e != nil || !worked {
		t.Fatal(worked, e)
	}
	var state string
	var attempts int
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "failed" || attempts != 1 {
		t.Fatal(state, attempts)
	}
	if worked, e = WorkOne(context.Background(), db, t.TempDir()); worked || e != nil {
		t.Fatal("permanent error retried", worked, e)
	}
	db.Exec(`UPDATE import_cover_jobs SET checked_at=0`)
	if e = RecheckStopped(context.Background(), db, 16); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT state FROM import_cover_jobs`).Scan(&state)
	if state != "failed" {
		t.Fatal("unchanged broken source rearmed")
	}
	os.WriteFile(path, []byte("a replacement with different contents and size"), 0600)
	db.Exec(`UPDATE import_cover_jobs SET checked_at=0`)
	if e = RecheckStopped(context.Background(), db, 16); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "pending" || attempts != 0 {
		t.Fatal(state, attempts)
	}
}

func TestCoverTransientBudgetAndLateSameSourceResult(t *testing.T) {
	db, path := retryFixture(t)
	calls := 0
	fail := func(context.Context, *sql.DB, string, int64, string, string) error {
		calls++
		return errors.New("PDF first page: signal killed")
	}
	for i := 0; i < 5; i++ {
		db.Exec(`UPDATE import_cover_jobs SET retry_at=0`)
		if _, e := workOne(context.Background(), db, "", fail); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := workOne(context.Background(), db, "", fail); e != nil {
		t.Fatal(e)
	}
	if calls != 5 {
		t.Fatal("unbounded retry", calls)
	}
	Queue(db, 1, path, "EPUB")
	late := func(context.Context, *sql.DB, string, int64, string, string) error {
		if e := Queue(db, 1, path, "EPUB"); e != nil {
			t.Fatal(e)
		}
		return errors.New("invalid PalmDB record bounds")
	}
	if _, e := workOne(context.Background(), db, "", late); e != nil {
		t.Fatal(e)
	}
	var state string
	var attempts int
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "pending" || attempts != 0 {
		t.Fatal("late result overwrote a new request", state, attempts)
	}
}

func TestLegacyCoverMigrationAndBookDeletion(t *testing.T) {
	db, path := retryFixture(t)
	db.Exec(`UPDATE import_cover_jobs SET error='invalid PalmDB record bounds',attempts=1091,state='pending'`)
	if e := MigrateQueue(db); e != nil {
		t.Fatal(e)
	}
	var state string
	var attempts int
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "failed" || attempts != 1091 {
		t.Fatal(state, attempts)
	}
	Queue(db, 1, path, "EPUB")
	db.Exec(`DELETE FROM books WHERE id=1`)
	if e := Queue(db, 1, path, "EPUB"); e != nil {
		t.Fatal(e)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM import_cover_jobs`).Scan(&count)
	if count != 0 {
		t.Fatal("deleted book retained/recreated cover work", count)
	}
}

func TestCoverUnavailableSourceRecoversWithoutConsumingBudget(t *testing.T) {
	db, path := retryFixture(t)
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if worked, e := WorkOne(context.Background(), db, t.TempDir()); !worked || e != nil {
		t.Fatal(worked, e)
	}
	var state string
	var attempts int
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "blocked" || attempts != 0 {
		t.Fatal(state, attempts)
	}
	db.Exec(`UPDATE import_cover_jobs SET checked_at=0`)
	if e := RecheckStopped(context.Background(), db, 16); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT state FROM import_cover_jobs`).Scan(&state)
	if state != "blocked" {
		t.Fatal("unavailable source rearmed")
	}
	if e := os.WriteFile(path, []byte("restored file"), 0600); e != nil {
		t.Fatal(e)
	}
	db.Exec(`UPDATE import_cover_jobs SET checked_at=0`)
	if e := RecheckStopped(context.Background(), db, 16); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT state,attempts FROM import_cover_jobs`).Scan(&state, &attempts)
	if state != "pending" || attempts != 0 {
		t.Fatal("restored source not resumed", state, attempts)
	}
}

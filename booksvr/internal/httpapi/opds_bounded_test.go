package httpapi

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/library"
	"testing"
	"time"
)

func TestOpdsClockAvoidsHistoricalScanAndTracksAtomicMutations(t *testing.T) {
	fx := newOpdsFixture(t)
	for range 2 {
		if err := library.EnsureOpdsClock(fx.DB); err != nil {
			t.Fatal(err)
		}
	}
	opdsSeedBook(t, fx.DB, 1, "Book", 1)
	for _, q := range []string{`UPDATE books SET title='Changed' WHERE id=1`, `INSERT INTO authors(id,name) VALUES(1,'Author')`, `INSERT INTO books_authors_link(book,author) VALUES(1,1)`, `DELETE FROM books WHERE id=1`} {
		fx.DB.Exec(`UPDATE book_opds_state SET updated_at=1`)
		if _, err := fx.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
		if opdsLastModified(fx.Cfg).Unix() < time.Now().Unix()-5 {
			t.Fatal("catalogue revision not updated", q)
		}
	}
	fx.DB.Exec(`UPDATE book_opds_state SET updated_at=1`)
	tx, err := fx.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE authors SET name='Rollback' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if opdsLastModified(fx.Cfg).Unix() != 1 {
		t.Fatal("rolled back mutation changed the feed revision")
	}
	// A malformed old timestamp cannot force a fallback scan once the clock exists.
	if _, err = fx.DB.Exec(`ALTER TABLE books RENAME COLUMN last_modified TO old_modified`); err != nil {
		t.Fatal(err)
	}
	if opdsLastModified(fx.Cfg).Unix() != 1 {
		t.Fatal("clock lookup touched historical book timestamps")
	}
}

func TestOpdsRootOnlyChecksCategoryExistence(t *testing.T) {
	fx := newOpdsFixture(t)
	// An item that cannot be decoded as Category must not be hydrated by root.
	if _, err := fx.DB.Exec(`DROP TABLE authors;CREATE TABLE authors(id TEXT,name TEXT);INSERT INTO authors VALUES('broken-id','a')`); err != nil {
		t.Fatal(err)
	}
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
	raw, err := opdsTopLevelFeed(fx.Cfg, ctx)
	if err != nil || !bytes.Contains(raw, []byte("By authors")) {
		t.Fatal(string(raw), err)
	}
}

func TestOpdsTitlePageUsesExistingIndexAndStableTies(t *testing.T) {
	fx := newOpdsFixture(t)
	if _, err := fx.DB.Exec(`CREATE INDEX idx_books_title ON books(title)`); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"a", "A", "A", "中文"} {
		opdsSeedBook(t, fx.DB, int64(i+1), title, 1)
	}
	ids, total, err := library.BrowseBooks(t.Context(), fx.DB, library.BrowseQuery{TitleSort: true, Start: 0, Size: 2})
	if err != nil || total != 4 || len(ids) != 2 || ids[0] != 3 || ids[1] != 2 {
		t.Fatal(ids, total, err)
	}
	rows, err := fx.DB.Query(`EXPLAIN QUERY PLAN SELECT b.id FROM books b WHERE 1=1 ORDER BY b.title,b.id DESC LIMIT 50 OFFSET 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var d string
		if err = rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d
	}
	if !strings.Contains(plan, "idx_books_title") || strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatal(plan)
	}
}

func TestOpdsCandidateSortingNeverReadsUnrelatedBooks(t *testing.T) {
	fx := newOpdsFixture(t)
	for i := 1; i <= 1002; i++ {
		opdsSeedBook(t, fx.DB, int64(i), fmt.Sprintf("Book %04d", i), int64(i))
	}
	fx.DB.Exec(`UPDATE books SET title=NULL WHERE id=1002`)
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	got, err := opdsOrderedIDs(fx.Cfg, ids, "title", true)
	if err != nil || len(got) != 1001 || got[0] != 1 || got[1000] != 1001 {
		t.Fatal(len(got), err)
	}
}

func TestOpdsSQLPagingFiltersBeforeLimitsAndOnlyHydratesPage(t *testing.T) {
	fx := newOpdsFixture(t)
	createFormatTables(t, fx.DB)
	for i := 1; i <= 65; i++ {
		opdsSeedBook(t, fx.DB, int64(i), fmt.Sprintf("Book %03d", i), int64(i))
	}
	if _, err := fx.DB.Exec(`CREATE TABLE source_sync_formats(format_id INTEGER,missing INTEGER);CREATE TABLE book_unavailable_sources(book_id INTEGER PRIMARY KEY);INSERT INTO book_unavailable_sources VALUES(65);INSERT INTO ratings(id,rating) VALUES(1,'bad');INSERT INTO books_ratings_link(book,rating) VALUES(33,1)`); err != nil {
		t.Fatal(err)
	}
	fx.Cfg.ExcludedBookIDs = func(_ *Context) (map[int64]bool, error) { return map[int64]bool{64: true}, nil }
	for _, order := range []string{"newest", "title"} {
		ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
		if order == "title" {
			fx.DB.Exec(`DELETE FROM books_ratings_link`)
		}
		raw, err := opdsAllBooks(fx.Cfg, ctx, order, "/opds/nav/test", "/opds/", 0)
		if err != nil {
			t.Fatal(order, err)
		}
		var feed atomFeedXML
		if err = xml.Unmarshal(raw, &feed); err != nil {
			t.Fatal(err)
		}
		if len(feed.Entries) != 30 || bytes.Contains(raw, []byte("Book 064")) || bytes.Contains(raw, []byte("Book 065")) || !bytes.Contains(raw, []byte("offset=60")) {
			t.Fatal(string(raw))
		}
		if order == "newest" && !strings.Contains(feed.Entries[0].Title, "063") {
			t.Fatal(feed.Entries[0])
		}
		raw, err = opdsAllBooks(fx.Cfg, ctx, order, "/opds/nav/test", "/opds/", 60)
		if order == "newest" {
			fx.DB.Exec(`DELETE FROM books_ratings_link`)
			raw, err = opdsAllBooks(fx.Cfg, ctx, order, "/opds/nav/test", "/opds/", 60)
		}
		if err != nil {
			t.Fatal(err)
		}
		feed = atomFeedXML{}
		if err = xml.Unmarshal(raw, &feed); err != nil || len(feed.Entries) != 3 {
			t.Fatal(string(raw), err)
		}
	}
}

func TestOpdsWiringPrivateAccessAndLiveSwitch(t *testing.T) {
	w, _, _ := referenceFixture(t)
	w.db.Exec(`UPDATE items SET scope='private',collector_id=2 WHERE book_id=1`)
	cfg := newOpdsConfig(w.libraryDB, w.db, ServeOptions{Settings: map[string]any{"OPDS_ENABLED": true}})
	for _, tc := range []struct {
		user        int64
		admin, want bool
	}{{0, false, true}, {1, false, true}, {2, false, false}, {1, true, false}} {
		ctx := pluginCtx(t, w.db, http.MethodGet, "/opds/", "", tc.user, tc.admin)
		ids, err := cfg.ExcludedBookIDs(ctx)
		if err != nil || ids[1] != tc.want {
			t.Fatal(tc, ids, err)
		}
	}
	p := filepath.Join(t.TempDir(), "auto.py")
	cfg.SettingsPath = p
	for _, enabled := range []bool{false, true} {
		value := "False"
		if enabled {
			value = "True"
		}
		if err := os.WriteFile(p, []byte("CONF = {\n 'OPDS_ENABLED': "+value+",\n}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		h := opdsHandler(cfg, func(_ *Context) ([]byte, error) { called = true; return []byte("ok"), nil })
		ctx := pluginCtx(t, w.db, http.MethodGet, "/opds/", "", 0, false)
		if _, err := h(ctx); err != nil || called != enabled {
			t.Fatal(enabled, called, err)
		}
	}
}

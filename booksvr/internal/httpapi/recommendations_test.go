package httpapi

import (
	"net/http"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
	"testing"
)

func TestRecommendationRankingInterestAndPopularity(t *testing.T) {
	books := []library.BookData{{ID: 1, Authors: []string{"刘慈欣"}}, {ID: 2, Authors: []string{"刘慈欣"}}, {ID: 3, Authors: []string{"其他"}, Rating: 10}, {ID: 4, Authors: []string{"未知"}}}
	states := map[int64]*userdb.ReadingState{1: {BookID: 1, Favorite: 1, ReadState: 2}}
	got := rankRecommendations(books, states, map[int64]int64{3: 100000}, 3, 0)
	if len(got) != 3 || got[0] != 2 {
		t.Fatal(got)
	}
	for _, id := range got {
		if id == 1 {
			t.Fatal("already read seed recommended")
		}
	}
	got = rankRecommendations(books, nil, map[int64]int64{3: 100000}, 1, 0)
	if got[0] != 3 {
		t.Fatal(got)
	}
	if len(rankRecommendations(books, nil, nil, 0, 0)) != 0 {
		t.Fatal("zero count")
	}
}

func TestRecommendationIndexProductionWiring(t *testing.T) {
	f := newBookFixture(t)
	for i := int64(1); i <= 5; i++ {
		insertBook(t, f.DB, i, "书")
	}
	cfg := newBookWiring(f.DB, f.DB, map[string]any{"MAIN_PAGE_RANDOM_COUNT": 2, "MAIN_PAGE_RECENT_COUNT": 3}, nil).Config("")
	ctx := pluginCtx(t, f.DB, http.MethodGet, "/api/index?random=200&recent=200", "", 0, false)
	got, err := BookIndexHandler(cfg)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["random_books_count"] != 2 || got["new_books_count"] != 3 {
		t.Fatal(got)
	}
	cfg.Settings["MAIN_PAGE_RANDOM_COUNT"] = 0
	got, err = BookIndexHandler(cfg)(ctx)
	if err != nil || got["recommendations_enabled"] != false || got["random_books_count"] != 0 {
		t.Fatal(got, err)
	}
}

func TestRecommendationIndexHidesPrivateMissingAndSortsTimestamp(t *testing.T) {
	f := newBookFixture(t)
	for i := int64(1); i <= 5; i++ {
		insertBook(t, f.DB, i, "书")
	}
	if _, err := f.DB.Exec(`CREATE TABLE book_unavailable_sources(book_id INTEGER PRIMARY KEY); INSERT INTO book_unavailable_sources VALUES(3); UPDATE books SET timestamp='2026-10-21 00:00:00' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	cfg := newBookWiring(f.DB, f.DB, map[string]any{}, nil).Config("")
	cfg.PrivateBookIDs = func(*Context) (map[int64]bool, error) { return map[int64]bool{2: true}, nil }
	ctx := pluginCtx(t, f.DB, http.MethodGet, "/api/index", "", 0, false)
	got, err := BookIndexHandler(cfg)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recent := got["new_books"].([]map[string]any)
	if len(recent) != 3 || int64Of(recent[0]["id"]) != 1 {
		t.Fatal(recent)
	}
	for _, key := range []string{"random_books", "new_books"} {
		for _, b := range got[key].([]map[string]any) {
			id := int64Of(b["id"])
			if id == 2 || id == 3 {
				t.Fatal("invisible title leaked", key, id)
			}
		}
	}
}

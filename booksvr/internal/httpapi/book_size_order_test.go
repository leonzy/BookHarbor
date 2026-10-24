package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"talebook/booksvr/internal/library"
)

func TestBookSizeOrderPreservesDatabasePage(t *testing.T) {
	f := newBookFixture(t)
	insertBook(t, f.DB, 10, "smaller")
	insertBook(t, f.DB, 20, "larger")
	cfg := f.Cfg
	cfg.PrivateBookIDs = func(*Context) (map[int64]bool, error) {
		return map[int64]bool{99: true}, nil
	}
	cfg.BrowsePage = func(ctx *Context, q library.BrowseQuery) ([]int64, int, error) {
		if q.SizeSort != "asc" || q.Start != 60 || q.Size != 2 || !q.Excluded[99] {
			t.Fatalf("query %+v", q)
		}
		return []int64{10, 20}, 100, nil
	}
	ctx := pluginCtx(t, f.DB, http.MethodGet, "/api/library?sort=size_asc&start=60&size=2", "", 1, false)
	got, err := BookLibraryHandler(cfg)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	books := got["books"].([]map[string]any)
	ids := []int64{}
	for _, b := range books {
		ids = append(ids, int64Of(b["id"]))
	}
	if !reflect.DeepEqual(ids, []int64{10, 20}) || got["total"] != 100 {
		t.Fatal(got)
	}
	if adminSizeOrder("size", true) != "desc" || adminSizeOrder("size", false) != "asc" || adminSizeOrder("title", true) != "" {
		t.Fatal("admin size sort")
	}
}

func TestBookSizeOrderShelfCompleteSet(t *testing.T) {
	f := newBookFixture(t)
	if err := library.Migrate(f.DB); err != nil {
		t.Fatal(err)
	}
	bookSeedReader(t, f.DB, 1, "owner")
	for _, id := range []int64{10, 20} {
		insertBook(t, f.DB, id, "shelf book")
		if _, err := f.DB.Exec(`INSERT INTO reading_state(book_id,reader_id,wants,read_state,online_read,download,progress) VALUES(?,1,1,1,0,0,'{}')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.DB.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(10,'EPUB','/books/a.epub',100,0),(20,'EPUB','/books/b.epub',200,0)`); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/shelf", "/api/reading"} {
		ctx := pluginCtx(t, f.DB, http.MethodGet, endpoint+"?sort=size_asc", "", 1, false)
		handler := BookShelfListHandler(f.Cfg)
		if endpoint == "/api/reading" {
			handler = BookReadingHandler(f.Cfg)
		}
		got, err := handler(ctx)
		if err != nil {
			t.Fatal(err)
		}
		books := got["books"].([]map[string]any)
		if len(books) != 2 || int64Of(books[0]["id"]) != 10 || int64Of(books[1]["id"]) != 20 {
			t.Fatal(endpoint, got)
		}
	}
}

func TestBookSizeOrderSavedNetworkSizes(t *testing.T) {
	book := library.BookData{ID: 1, Formats: []string{"EPUB"}, FormatSizes: map[string]int64{"EPUB": 4096}}
	got := formatBookData(&Context{}, NetworkConfig{}, &book)
	if !reflect.DeepEqual(got["format_sizes"], book.FormatSizes) {
		t.Fatal(got)
	}
}

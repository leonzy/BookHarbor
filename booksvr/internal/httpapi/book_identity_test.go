package httpapi

import (
	"net/http"
	"talebook/booksvr/internal/library"
	"testing"
)

func TestIdentityCardFormatsAggregateVisibleMembers(t *testing.T) {
	fx := newBookFixture(t)
	if e := library.Migrate(fx.DB); e != nil {
		t.Fatal(e)
	}
	m := library.BookMeta{Title: "逻辑思考", Authors: []string{"作者"}, ISBN: "9780306406157"}
	ids := []int64{}
	for _, f := range []string{"EPUB", "MOBI", "PDF"} {
		id, e := library.ImportBook(fx.DB, m)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		if e = library.AddFormat(fx.DB, id, f, "/private/"+f, 256, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := library.ReconcileBookIdentity(fx.DB, ids[0], false); e != nil {
		t.Fatal(e)
	}
	fx.Cfg.CanViewBook = func(_ *Context, id int64) bool { return id != ids[2] }
	ctx := netCtx(t, fx.DB, http.MethodGet, "/api/library", "", 0, false)
	cards := []map[string]any{{"id": ids[0]}}
	if e := bookAttachIdentityCards(fx.Cfg, ctx, cards); e != nil {
		t.Fatal(e)
	}
	sizes := cards[0]["format_sizes"].(map[string]int64)
	if len(sizes) != 2 || sizes["EPUB"] != 256 || sizes["MOBI"] != 256 || cards[0]["version_count"] != 2 {
		t.Fatal(cards)
	}
	if cards[0]["files"] != nil || cards[0]["storage"] != nil {
		t.Fatal("card leaked files", cards)
	}
}

func TestBookIdentityVersionsFiltersPrivateBooksAndPaths(t *testing.T) {
	fx := newBookFixture(t)
	if e := library.Migrate(fx.DB); e != nil {
		t.Fatal(e)
	}
	m := library.BookMeta{Title: "同一作品", Authors: []string{"作者"}, ISBN: "9780306406157"}
	a, e := library.ImportBook(fx.DB, m)
	if e != nil {
		t.Fatal(e)
	}
	b, e := library.ImportBook(fx.DB, m)
	if e != nil {
		t.Fatal(e)
	}
	hidden, e := library.ImportBook(fx.DB, m)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []int64{a, b, hidden} {
		if e = library.AddFormat(fx.DB, id, "EPUB", "/private-directory/source.epub", 256, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = library.ReconcileBookIdentity(fx.DB, a, false); e != nil {
		t.Fatal(e)
	}
	fx.Cfg.CanViewBook = func(_ *Context, id int64) bool { return id != hidden }
	ctx := netCtx(t, fx.DB, http.MethodGet, "/api/book/1", "", 0, false)
	list, e := bookIdentityVersions(fx.Cfg, ctx, a)
	if e != nil || len(list) != 1 || list[0]["id"] != b {
		t.Fatal(list, e)
	}
	files := list[0]["files"].([]map[string]any)
	if len(files) != 1 || files[0]["storage"] != nil {
		t.Fatal("guest path leaked", files)
	}
	ctx.Identity.UserID = 1
	ctx.Identity.IsAdmin = true
	list, e = bookIdentityVersions(fx.Cfg, ctx, a)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	files = list[0]["files"].([]map[string]any)
	if files[0]["storage"] == nil {
		t.Fatal("admin storage absent", files)
	}
}

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"talebook/booksvr/internal/access"
	"talebook/booksvr/internal/library"
)

func TestAccessBookDetailUsesOnlyAuthorizedFormats(t *testing.T) {
	f := newBookFixture(t)
	insertBook(t, f.DB, 10, "Visible EPUB")
	insertBook(t, f.DB, 20, "Hidden book")
	bookSeedReader(t, f.DB, 1, "reader")
	if err := library.MigrateAccess(f.DB); err != nil {
		t.Fatal(err)
	}
	s := library.AccessStore{DB: f.DB}
	c := context.Background()
	actor, err := s.RegisterPrincipal(c, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(c, 0, 0, 0, actor, access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.Selected, Targets: []access.Target{{Root: "root", Directory: "/public", Recursive: true}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(c, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := library.AccessBuildToken{Revision: r.ID, Version: r.Version}
	profiles, err := s.BuildAccessProfiles(c, token, []string{actor})
	if err != nil {
		t.Fatal(err)
	}
	file := func(id, format, dir string, size int64) library.AccessFileSnapshot {
		return library.AccessFileSnapshot{ID: id, Format: format, Size: size, Version: 1, Facts: access.FileFacts{Root: "root", Directory: dir, Ready: true}}
	}
	if err = s.WriteAccessBatch(c, token, profiles, []library.AccessBookSnapshot{
		{ID: 10, Title: "Visible EPUB", Files: []library.AccessFileSnapshot{file("epub", "EPUB", "/public", 10), file("mobi", "MOBI", "/private", 100000)}},
		{ID: 20, Title: "Hidden book", Files: []library.AccessFileSnapshot{file("secret", "EPUB", "/private", 20)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.DB.Exec(`INSERT INTO acl_build(revision,version,phase) VALUES(?,?,'delta')`, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPreparation(c, token); err != nil {
		t.Fatal(err)
	}
	scope, err := s.PreviewAccess(c, r.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	ctx := pluginCtx(t, f.DB, http.MethodGet, "/api/book/10", "", 1, false)
	ctx.Params = []string{"10"}
	ctx.Access = &scope
	feed, err := opdsAllBooks(OpdsConfig{DB: f.DB}, ctx, "title", "/opds/nav/title", "/opds/", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feed), "Visible EPUB") || strings.Contains(string(feed), "Hidden book") || strings.Contains(strings.ToLower(string(feed)), ".mobi") {
		t.Fatal("OPDS exposed hidden book or format", string(feed))
	}
	if _, err = opdsSearch(OpdsConfig{DB: f.DB}, ctx, "format:MOBI", 0); err != errOpdsNoBooks {
		t.Fatal("OPDS search exposed hidden format", err)
	}
	resp, err := BookDetailHandler(f.Cfg)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, ok := resp["book"].(map[string]any)
	if !ok {
		t.Fatal(resp)
	}
	files := b["files"].([]map[string]any)
	if len(files) != 1 || files[0]["format"] != "EPUB" || files[0]["size"] != int64(10) {
		t.Fatal("hidden format or unscoped size exposed", files)
	}
	ctx.Params = []string{"20"}
	resp, err = BookDetailHandler(f.Cfg)(ctx)
	if err != nil || resp["err"] != "params.book.invalid" {
		t.Fatal("hidden detail exposed", resp, err)
	}
}

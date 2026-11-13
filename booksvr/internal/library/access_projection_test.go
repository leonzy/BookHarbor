package library

import (
	"context"
	"errors"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessProjectionMixedFormatsPrivateCountsAndCancellation(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	other, err := s.RegisterPrincipal(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	p := access.Policy{Groups: []access.Group{{ID: "readers", Name: "读者", Members: []string{actor, other}, Range: access.Ceiling{Mode: access.Selected, Targets: []access.Target{{Root: "root", Directory: "/public", Recursive: true}}}}}}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := AccessBuildToken{Revision: r.ID, Version: r.Version}
	profiles, err := s.BuildAccessProfiles(ctx, token, []string{actor, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatal("same permissions created per-user full copies")
	}
	file := func(id, format, dir string, size int64) AccessFileSnapshot {
		return AccessFileSnapshot{ID: id, Format: format, Size: size, Version: 1, Facts: access.FileFacts{Root: "root", Directory: dir, Ready: true}}
	}
	private := file("private", "EPUB", "/public", 30)
	private.Facts.Private = true
	private.Facts.PrivateOwner = actor
	books := []AccessBookSnapshot{
		{ID: 1, Title: "公开 EPUB", Files: []AccessFileSnapshot{file("epub", "EPUB", "/public", 10), file("mobi", "MOBI", "/secret", 100000)}},
		{ID: 2, Title: "本人私藏", Files: []AccessFileSnapshot{private}},
		{ID: 3, Title: "另一本", Files: []AccessFileSnapshot{file("third", "PDF", "/public", 20)}},
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, books); err != nil {
		t.Fatal(err)
	}
	// Retry the same batch must replace, not double count or duplicate formats.
	if err = s.WriteAccessBatch(ctx, token, profiles, books); err != nil {
		t.Fatal(err)
	}
	accessTestCheckpoint(t, s, token)
	if err = s.FinishPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id    string
		total int64
		first int64
	}{{actor, 3, 2}, {other, 2, 3}} {
		scope, e := s.PreviewAccess(ctx, r.ID, tt.id)
		if e != nil {
			t.Fatal(e)
		}
		page, total, e := s.AccessPreviewPage(ctx, scope, 0, 10, "size")
		if e != nil {
			t.Fatal(e)
		}
		if total != tt.total || len(page) != int(tt.total) || page[0].ID != tt.first {
			t.Fatalf("wrong visible sizes/counts: %+v %d", page, total)
		}
		counts, e := AccessSummary(ctx, s.DB, scope)
		if e != nil || counts["books"] != tt.total {
			t.Fatal("summary leaked private books", counts, e)
		}
		ids, e := AccessFilterIDs(ctx, s.DB, scope, []int64{3, 2, 999, 1})
		if e != nil || len(ids) != int(tt.total) || ids[0] != 3 || ids[len(ids)-1] != 1 {
			t.Fatal("candidate filter lost scope/order", ids, e)
		}
		ok, e := s.AccessCanReadFile(ctx, scope, "mobi", 1)
		if e != nil || ok {
			t.Fatal("restricted MOBI leaked", e)
		}
		ok, e = s.AccessCanReadFile(ctx, scope, "epub", 1)
		if e != nil || !ok {
			t.Fatal("public EPUB missing", e)
		}
		ok, e = s.AccessCanReadFile(ctx, scope, "private", 1)
		if e != nil || ok != (tt.id == actor) {
			t.Fatal("private file leaked", e)
		}
		ok, e = s.AccessCanReadFile(ctx, scope, "epub", 2)
		if e != nil || ok {
			t.Fatal("wrong source revision authorized", e)
		}
	}
	scope, err := s.PreviewAccess(ctx, r.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, books); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("stale worker wrote after cancellation", err)
	}
	if _, _, err = s.AccessPreviewPage(ctx, scope, 0, 10, "size"); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("cancelled preview served", err)
	}
}

func TestAccessProjectionRemovalUpdatesExactCounts(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := AccessBuildToken{Revision: r.ID, Version: r.Version}
	profiles, err := s.BuildAccessProfiles(ctx, token, []string{actor})
	if err != nil {
		t.Fatal(err)
	}
	b := AccessBookSnapshot{ID: 1, Title: "a", Files: []AccessFileSnapshot{{ID: "f", Format: "TXT", Version: 1, Size: 100, Facts: access.FileFacts{Root: "root", Directory: "/", Ready: true}}}}
	if err = s.WriteAccessBatch(ctx, token, profiles, []AccessBookSnapshot{b}); err != nil {
		t.Fatal(err)
	}
	b.Files = nil
	if err = s.WriteAccessBatch(ctx, token, profiles, []AccessBookSnapshot{b}); err != nil {
		t.Fatal(err)
	}
	accessTestCheckpoint(t, s, token)
	if err = s.FinishPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	scope, err := s.PreviewAccess(ctx, r.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	page, total, err := s.AccessPreviewPage(ctx, scope, 0, 10, "newest")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 || total != 0 {
		t.Fatal("deleted source remains visible", page, total)
	}
	if _, err = s.AccessCanReadFile(ctx, AccessScope{}, "f", 1); !errors.Is(err, ErrAccessNotReady) {
		t.Fatal("zero scope not rejected", err)
	}
}

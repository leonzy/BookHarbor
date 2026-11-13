package library

import (
	"context"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessGroupedCounterPrivateOverlayAndRegroup(t *testing.T) {
	db := newQueryDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := AccessStore{DB: db}
	actor, err := s.RegisterPrincipal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.RegisterPrincipal(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE books SET work_id=100 WHERE id IN (1,2); INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature,managed_work) VALUES(1,'','same','author','a',100),(2,'','same','author','b',100)`); err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
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
	books := []AccessBookSnapshot{}
	for i := int64(1); i <= 3; i++ {
		facts := access.FileFacts{Root: "root", Directory: "/public", Ready: true}
		if i == 1 {
			facts.Private = true
			facts.PrivateOwner = actor
		}
		books = append(books, AccessBookSnapshot{ID: i, Title: "book", Files: []AccessFileSnapshot{{ID: string(rune('a' + i)), Format: "EPUB", Version: 1, Facts: facts}}})
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, books); err != nil {
		t.Fatal(err)
	}
	accessTestCheckpoint(t, s, token)
	if err = s.FinishPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{actor, other} {
		scope, e := s.PreviewAccess(ctx, r.ID, user)
		if e != nil {
			t.Fatal(e)
		}
		ids, total, e := AccessBrowseBooks(ctx, db, scope, AccessBrowseQuery{BrowseQuery: BrowseQuery{GroupWorks: true, Size: 20}})
		if e != nil || total != 2 || len(ids) != 2 {
			t.Fatal("private work overlay count", ids, total, e)
		}
		want := int64(2)
		if user == actor {
			want = 1
		}
		if ids[1] != want {
			t.Fatal("hidden representative selected", ids, user)
		}
	}
	if _, err = db.Exec(`UPDATE book_identity_keys SET managed_work=0 WHERE book_id=1`); err != nil {
		t.Fatal(err)
	}
	scope, err := s.PreviewAccess(ctx, r.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	ids, total, err := AccessBrowseBooks(ctx, db, scope, AccessBrowseQuery{BrowseQuery: BrowseQuery{GroupWorks: true, Size: 20}})
	if err != nil || total != 3 || len(ids) != 3 {
		t.Fatal("regroup did not update counters", ids, total, err)
	}
}

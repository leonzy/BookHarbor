package library

import (
	"context"
	"errors"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessClassDraftAssignmentIsolationAndRollback(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT); INSERT INTO books VALUES(1,'甲'),(2,'乙')`); err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{Classes: []access.Class{{ID: "fiction", Name: "小说"}}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.AssignAccessClass(ctx, r.ID, r.Version, "fiction", []int64{1, 2}, true, actor)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != r.Version+1 {
		t.Fatal("version not advanced")
	}
	if _, err = s.AssignAccessClass(ctx, r.ID, r.Version, "fiction", []int64{1}, false, actor); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("stale edit accepted", err)
	}
	if _, err = s.AssignAccessClass(ctx, r.ID, updated.Version, "fiction", []int64{1, 999}, false, actor); err == nil {
		t.Fatal("missing book accepted")
	}
	books, total, err := s.AccessClassBooks(ctx, r.ID, "fiction", 0)
	if err != nil || total != 2 || len(books) != 2 {
		t.Fatal("partial failed edit leaked", books, total, err)
	}
	var live int
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_class_membership`).Scan(&live); err != nil || live != 0 {
		t.Fatal("draft modified live membership", live, err)
	}
	if _, err = s.DB.Exec(`DELETE FROM books WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	books, total, err = s.AccessClassBooks(ctx, r.ID, "fiction", 0)
	if err != nil || total != 1 || len(books) != 1 {
		t.Fatal("deleted book counted", books, total, err)
	}
	if _, err = s.AssignAccessClass(ctx, r.ID, updated.Version, "fiction", []int64{1}, false, "unknown"); err == nil {
		t.Fatal("unknown actor accepted")
	}
}

package library

import (
	"context"
	"errors"
	"strings"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessInvalidationUpgradesExistingTriggerAndUsesBookIndex(t *testing.T) {
	s, _ := accessTestStore(t)
	if _, err := s.DB.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT,timestamp TEXT,path TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(s.DB); err != nil {
		t.Fatal(err)
	}
	const trigger = "acl_invalidate_book_path"
	var installed string
	readTrigger := func() {
		t.Helper()
		if err := s.DB.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, trigger).Scan(&installed); err != nil {
			t.Fatal(err)
		}
	}
	readTrigger()
	previous := strings.ReplaceAll(installed, " INDEXED BY acl_profile_file_invalidate", "")
	if _, err := s.DB.Exec("DROP TRIGGER " + trigger + "; " + previous); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(s.DB); err != nil {
		t.Fatal(err)
	}
	readTrigger()
	if installed == previous {
		t.Fatal("deployed trigger retained the old query")
	}
	for _, stmt := range strings.Split(accessInvalidationSQL("42"), ";") {
		if !strings.Contains(stmt, "UPDATE acl_profile_count") {
			continue
		}
		rows, err := s.DB.Query("EXPLAIN QUERY PLAN " + stmt)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(detail, "acl_profile_file_invalidate (book_id=?") {
				return
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("file count does not seek to the book ID")
}

func TestAccessInvalidationIsAtomicAndIgnoresMetadataOnlyChanges(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT,timestamp TEXT,path TEXT); INSERT INTO books VALUES(1,'one','2026-01-01','public')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(s.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE books SET path='public-before-configuration' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	dirty, err := s.AccessDirtyBatch(ctx, 64)
	if err != nil || len(dirty) != 0 {
		t.Fatal("inactive ACL queued work", dirty, err)
	}
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
	b := AccessBookSnapshot{ID: 1, Title: "one", Files: []AccessFileSnapshot{{ID: "f", Format: "TXT", Version: 1, Size: 100, Facts: access.FileFacts{Root: "root", Directory: "/", Ready: true}}}}
	if err = s.WriteAccessBatch(ctx, token, profiles, []AccessBookSnapshot{b}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE books SET title='new title',timestamp='2026-02-01' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var title string
	if err = s.DB.QueryRow(`SELECT title FROM acl_profile_book WHERE book_id=1`).Scan(&title); err != nil || title != "new title" {
		t.Fatal("metadata update hid book", title, err)
	}
	dirty, err = s.AccessDirtyBatch(ctx, 64)
	if err != nil || len(dirty) != 0 {
		t.Fatal("metadata update enqueued ACL rebuild", dirty, err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE books SET path='secret' WHERE id=1`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_profile_file`).Scan(&n); err != nil || n != 1 {
		t.Fatal("rollback lost permissions", n, err)
	}
	if _, err = s.DB.Exec(`UPDATE books SET path='secret' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_profile_file`).Scan(&n); err != nil || n != 0 {
		t.Fatal("old file permission survived move", n, err)
	}
	if err = s.DB.QueryRow(`SELECT sum(books)+sum(files) FROM acl_profile_count`).Scan(&n); err != nil || n != 0 {
		t.Fatal("counts did not revoke atomically", n, err)
	}
	dirty, err = s.AccessDirtyBatch(ctx, 64)
	if err != nil || len(dirty) != 1 || dirty[0].BookID != 1 {
		t.Fatal("move not queued", dirty, err)
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, []AccessBookSnapshot{b}); !errors.Is(err, ErrAccessNotReady) {
		t.Fatal("stale snapshot resurrected grant", err)
	}
	if _, err = s.DB.Exec(`UPDATE books SET path=path WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	again, err := s.AccessDirtyBatch(ctx, 64)
	if err != nil || again[0].Version != dirty[0].Version {
		t.Fatal("no-op write dirtied access", again, err)
	}
}

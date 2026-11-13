package library

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessFormatRegistrationInheritsPrivateSourceAtomically(t *testing.T) {
	db := newQueryDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s := AccessStore{DB: db}
	if _, err := s.ObserveAccessRoot(context.Background(), root, "test", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO acl_book_owner VALUES(1,'original',1)`); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFormat(db, 1, "EPUB", filepath.Join(root, "secret", "one.epub"), 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFormat(db, 2, "EPUB", filepath.Join(root, "public", "two.epub"), 100, 1, InheritAccessSource(1, "EPUB", 2, "EPUB", root)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT facts FROM acl_format_lineage WHERE book_id=2 AND format='EPUB'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var parents []access.FileFacts
	if err := json.Unmarshal([]byte(raw), &parents); err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0].Directory != "/secret" || !parents[0].Private || parents[0].PrivateOwner != "original" {
		t.Fatal("source restriction lost", raw)
	}
	if err := RegisterFormat(db, 2, "PDF", filepath.Join(root, "public", "two.pdf"), 100, 1, InheritAccessSource(999, "PDF", 2, "PDF", root)); err == nil {
		t.Fatal("unknown parent accepted")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM book_formats WHERE book_id=2 AND format='PDF'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed guard registered output", n, err)
	}
}

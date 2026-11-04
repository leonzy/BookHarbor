package scan

import (
	"context"
	"database/sql"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
	"testing"
)

func identityImportFixture(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, lib := importTestDB(t), importLibraryDB(t)
	db.SetMaxOpenConns(1)
	lib.SetMaxOpenConns(1)
	for _, q := range []string{
		`ALTER TABLE books ADD COLUMN work_id INTEGER`, `ALTER TABLE books ADD COLUMN title_norm TEXT`, `ALTER TABLE books ADD COLUMN author_norm TEXT`,
		`CREATE TABLE works(id INTEGER PRIMARY KEY,title_norm TEXT,author_norm TEXT,work_key TEXT UNIQUE,display_title TEXT,display_author TEXT,created_at INTEGER,updated_at INTEGER)`,
		`CREATE TABLE identifiers(id INTEGER PRIMARY KEY,book INTEGER,type TEXT,val TEXT)`,
		`CREATE TABLE publishers(id INTEGER PRIMARY KEY,name TEXT)`, `CREATE TABLE books_publishers_link(id INTEGER PRIMARY KEY,book INTEGER,publisher INTEGER)`,
		`CREATE TABLE languages(id INTEGER PRIMARY KEY,lang_code TEXT)`, `CREATE TABLE books_languages_link(id INTEGER PRIMARY KEY,book INTEGER,lang_code INTEGER,item_order INTEGER)`,
	} {
		if _, e := lib.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if e := library.EnsureIdentitySchema(lib); e != nil {
		t.Fatal(e)
	}
	return newImportService(t, db, lib), lib
}
func TestIdentityImportAddsMissingFormatAndRetainsSameFormatFiles(t *testing.T) {
	s, lib := identityImportFixture(t)
	id, e := library.ImportBook(lib, library.BookMeta{Title: "微神集 月牙集", Authors: []string{"老舍"}, ISBN: "9787020091621"})
	if e != nil {
		t.Fatal(e)
	}
	if e = library.AddFormat(lib, id, "EPUB", "/original.epub", 200, 0); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	text := "书名：微神集 月牙集\n作者：老舍\nISBN：9787020091621\n正文"
	path := writeBook(t, dir, "first.txt", []byte(text))
	seedReadyRow(t, s.DB, path, "isbn-first")
	r, e := s.Import(nil, 1, false, ModeIndex)
	if e != nil || r.Indexed != 1 {
		t.Fatal(r, e)
	}
	var files int
	lib.QueryRow(`SELECT count(*) FROM book_formats WHERE book_id=?`, id).Scan(&files)
	if files != 2 {
		t.Fatal("missing format did not reuse book", files)
	}
	path = writeBook(t, dir, "second.txt", []byte(text+"\n更完整正文"))
	seedReadyRow(t, s.DB, path, "isbn-second")
	r, e = s.Import(nil, 1, false, ModeIndex)
	if e != nil || r.Indexed != 1 || r.Exist != 0 {
		t.Fatal(r, e)
	}
	var books int
	lib.QueryRow(`SELECT count(*) FROM books`).Scan(&books)
	lib.QueryRow(`SELECT count(*) FROM book_formats`).Scan(&files)
	if books != 2 || files != 3 {
		t.Fatal("same format lost", books, files)
	}
	if _, e = library.ReconcileBookIdentity(lib, id, false); e != nil {
		t.Fatal(e)
	}
	siblings, e := library.IdentityVersions(lib, id)
	if e != nil || len(siblings) != 1 {
		t.Fatal(siblings, e)
	}
	// The scan stage also must not drop another same-name file before rich match.
	found, e := s.markExisting(nil, importmeta.Metadata{Title: "微神集 月牙集", Authors: []string{"老舍"}})
	if e != nil || found {
		t.Fatal(found, e)
	}
	if _, e = library.RunIdentityBatch(context.Background(), lib, "test-import", 5); e != nil {
		t.Fatal(e)
	}
}
func TestIdentityImportDoesNotReuseOtherUsersPrivateBook(t *testing.T) {
	s, lib := identityImportFixture(t)
	m := importmeta.Metadata{Title: "私藏作品", Authors: []string{"作者"}, ISBN: "9780306406157"}
	id, e := library.ImportBook(lib, library.BookMeta{Title: m.Title, Authors: m.Authors, ISBN: m.ISBN})
	if e != nil {
		t.Fatal(e)
	}
	s.DB.Exec(`INSERT INTO items(book_id,scope,collector_id) VALUES(?,'private',2)`, id)
	match, _, e := s.findSameAuthorBook(m, "TXT", 1)
	if e != nil || match != 0 {
		t.Fatal("private book reused", match, e)
	}
	match, _, e = s.findSameAuthorBook(m, "TXT", 2)
	if e != nil || match != id {
		t.Fatal(match, e)
	}
}

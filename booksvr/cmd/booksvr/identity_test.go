package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestBookIdentityPreviewIsReadOnlyThenApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT,author_sort TEXT,pubdate TEXT,work_id INTEGER,title_norm TEXT,author_norm TEXT)`,
		`CREATE TABLE authors(id INTEGER PRIMARY KEY,name TEXT)`, `CREATE TABLE books_authors_link(id INTEGER PRIMARY KEY,book INTEGER,author INTEGER)`,
		`CREATE TABLE identifiers(id INTEGER PRIMARY KEY,book INTEGER,type TEXT,val TEXT)`,
		`CREATE TABLE publishers(id INTEGER PRIMARY KEY,name TEXT)`, `CREATE TABLE books_publishers_link(id INTEGER PRIMARY KEY,book INTEGER,publisher INTEGER)`,
		`CREATE TABLE languages(id INTEGER PRIMARY KEY,lang_code TEXT)`, `CREATE TABLE books_languages_link(id INTEGER PRIMARY KEY,book INTEGER,lang_code INTEGER)`,
		`CREATE TABLE works(id INTEGER PRIMARY KEY,title_norm TEXT,author_norm TEXT,work_key TEXT UNIQUE,display_title TEXT,display_author TEXT,created_at INTEGER,updated_at INTEGER)`,
		`INSERT INTO books(id,title,author_sort) VALUES(1,'测试作品','作者'),(2,'测试作品','作者')`,
		`INSERT INTO identifiers(book,type,val) VALUES(1,'isbn','9780306406157'),(2,'isbn','0-306-40615-2')`,
	} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	var out bytes.Buffer
	if e = runBookIdentity([]string{"-db", path, "-ids", "1"}, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"Members":[1,2]`) {
		t.Fatal(out.String())
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='book_identity_keys'`).Scan(&n)
	if n != 0 {
		t.Fatal("preview created schema")
	}
	out.Reset()
	if e = runBookIdentity([]string{"-db", path, "-ids", "1", "-apply"}, &out); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT count(DISTINCT work_id) FROM books`).Scan(&n)
	if n != 1 {
		t.Fatal("not grouped", n)
	}
}

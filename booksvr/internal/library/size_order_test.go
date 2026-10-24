package library

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBookSizeOrder(t *testing.T) {
	db := newQueryDB(t)
	ctx := context.Background()
	run := func(stmt string) {
		t.Helper()
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(stmt, err)
		}
	}
	run(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(1,1,'EPUB','/books/one.epub',20,0),(2,1,'PDF','/books/one.pdf',200,0),(3,2,'TXT','/books/two.txt',100,0)`)
	books, err := GetBooksData(db, []int64{1})
	if err != nil || len(books) != 1 || books[0].FormatSizes["EPUB"] != 20 || books[0].FormatSizes["PDF"] != 200 {
		t.Fatal("displayed source sizes disagree with sorting", books, err)
	}
	check := func(q BrowseQuery, want []int64) {
		t.Helper()
		q.Size = 10
		ids, _, err := BrowseBooks(ctx, db, q)
		if err != nil || !reflect.DeepEqual(ids, want) {
			t.Fatalf("got %v want %v: %v", ids, want, err)
		}
	}
	check(BrowseQuery{SizeSort: "asc"}, []int64{2, 1, 3})
	check(BrowseQuery{SizeSort: "desc"}, []int64{1, 2, 3})
	run(`INSERT INTO data(book,format,uncompressed_size,name) VALUES(2,'EPUB',100,'two')`)
	check(BrowseQuery{SizeSort: "asc", Format: "EPUB"}, []int64{2, 1})
	check(BrowseQuery{SizeSort: "desc", Format: "EPUB"}, []int64{1, 2})
	check(BrowseQuery{SizeSort: "desc", Format: "EPUB", Excluded: map[int64]bool{1: true}}, []int64{2})
	ids, n, err := BrowseBooks(ctx, db, BrowseQuery{SizeSort: "asc", Start: 1, Size: 1})
	if err != nil || n != 3 || !reflect.DeepEqual(ids, []int64{1}) {
		t.Fatal(ids, n, err)
	}
	check(BrowseQuery{SizeSort: "desc", Excluded: map[int64]bool{1: true}}, []int64{2, 3})
	check(BrowseQuery{SizeSort: "asc", Category: "authors", Names: []string{"刘慈欣"}}, []int64{2, 1})
	run(`UPDATE book_formats SET size=100 WHERE id=2`)
	check(BrowseQuery{SizeSort: "desc"}, []int64{2, 1, 3})
	run(`INSERT INTO source_sync_formats(format_id,identity,size,mtime,missing) VALUES(2,'x',100,0,1)`)
	check(BrowseQuery{SizeSort: "asc"}, []int64{1, 2, 3})
	run(`UPDATE source_sync_formats SET missing=0 WHERE format_id=2`)
	run(`DELETE FROM book_formats WHERE id=2`)
	check(BrowseQuery{SizeSort: "desc"}, []int64{2, 1, 3})
	run(`DELETE FROM book_formats WHERE id=1`)
	check(BrowseQuery{SizeSort: "desc"}, []int64{1, 2, 3}) // Calibre size is now the fallback.
	run(`UPDATE data SET uncompressed_size=10 WHERE book=1`)
	check(BrowseQuery{SizeSort: "asc"}, []int64{1, 2, 3})
	run(`INSERT INTO books(id,title) VALUES(4,'new')`)
	check(BrowseQuery{SizeSort: "asc"}, []int64{1, 2, 4, 3})
	run(`DELETE FROM books WHERE id=4`)
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	check(BrowseQuery{SizeSort: "asc"}, []int64{1, 2, 3})
	for _, direction := range []string{"asc", "desc"} {
		rows, e := db.Query(`EXPLAIN QUERY PLAN SELECT z.book_id FROM book_sort_sizes z ORDER BY ` + sizeOrderSQL(direction, "z") + ` LIMIT 10`)
		if e != nil {
			t.Fatal(e)
		}
		plan := ""
		for rows.Next() {
			var a, b, c int
			var s string
			if e = rows.Scan(&a, &b, &c, &s); e != nil {
				t.Fatal(e)
			}
			plan += s
		}
		rows.Close()
		if !strings.Contains(plan, "book_size_"+direction) || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatal(plan)
		}
	}
	if SizeOrder("size_asc") != "asc" || SizeOrder("size_desc") != "desc" || SizeOrder("size; DROP TABLE books") != "" {
		t.Fatal("invalid sort whitelist")
	}
}

func TestBookSizeOrderLegacyFormatsWithoutSize(t *testing.T) {
	db := newQueryDB(t)
	if _, err := db.Exec(`DROP TABLE book_formats; CREATE TABLE book_formats(id INTEGER PRIMARY KEY,book_id INTEGER,format TEXT,path TEXT)`); err != nil {
		t.Fatal(err)
	}
	books, err := GetBooksData(db, []int64{1})
	if err != nil || len(books) != 1 || books[0].FormatSizes["EPUB"] != 1000 {
		t.Fatal("legacy formats must keep Calibre sizes", books, err)
	}
}

func TestBookSizeOrderPrivateMissingCount(t *testing.T) {
	db := newQueryDB(t)
	if _, err := db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(3,3,'TXT','/books/three.txt',100,0); INSERT INTO source_sync_formats(format_id,identity,size,mtime,missing) VALUES(3,'x',100,0,1)`); err != nil {
		t.Fatal(err)
	}
	ids, total, err := BrowseBooks(context.Background(), db, BrowseQuery{SizeSort: "asc", Size: 10, Excluded: map[int64]bool{3: true, 2: false}})
	if err != nil || total != 2 || !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Fatal("private and missing must not be subtracted twice", ids, total, err)
	}
}

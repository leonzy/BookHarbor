package library

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCatalogSearchLargePendingTail(t *testing.T) {
	db := newQueryDB(t)
	if _, err := db.Exec(`INSERT INTO books(id,title,author_sort,path) VALUES(100000,'西游记','作者','')`); err != nil {
		t.Fatal(err)
	}
	q := CatalogSearchQuery{Keyword: "西游记", BrowseQuery: BrowseQuery{Size: 1}}
	ids, total, err := CatalogSearchPage(context.Background(), db, q)
	if err != nil || total != 1 || !slices.Equal(ids, []int64{100000}) {
		t.Fatal(ids, total, err)
	}
}

func TestCatalogSearchTitleIndexLifecycle(t *testing.T) {
	db := newQueryDB(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`UPDATE books SET title=CASE id WHEN 1 THEN '雍正大传' WHEN 2 THEN '雍正大传' ELSE '西游记' END`,
		`INSERT INTO works(id,title_norm,author_norm,work_key,display_title,display_author,created_at,updated_at) VALUES(100,'test','test','test','test','test',0,0)`,
		`UPDATE books SET work_id=100 WHERE id IN (1,2)`,
		`INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature,managed_work) VALUES(1,'','test','author','a',100),(2,'','test','author','b',100)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(q CatalogSearchQuery, ids []int64, total int) {
		t.Helper()
		got, n, err := CatalogSearchPage(ctx, db, q)
		if err != nil || n != total || !slices.Equal(got, ids) {
			t.Fatalf("%q: ids=%v total=%d err=%v, want %v %d", q.Keyword, got, n, err, ids, total)
		}
	}
	q := CatalogSearchQuery{Keyword: "雍正", BrowseQuery: BrowseQuery{Size: 1}}
	expect(q, []int64{2}, 1) // Unindexed rows are still searchable.
	index := func() {
		t.Helper()
		if worked, err := IndexTitleBatch(ctx, db); err != nil || !worked {
			t.Fatal(worked, err)
		}
	}
	index()
	expect(q, []int64{2}, 1)
	expect(CatalogSearchQuery{Keyword: "西游记", BrowseQuery: BrowseQuery{Size: 1}}, []int64{3}, 1)
	if err := MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO acl_profile_book(revision,profile,book_id,title,timestamp,size,owner)
		VALUES(1,'reader',1,'雍正大传','',10,''),(1,'reader',2,'雍正大传','',1000,'other')`); err != nil {
		t.Fatal(err)
	}
	scoped := q
	scoped.Scope = &AccessScope{valid: true, revision: 1, profile: "reader", principal: "me"}
	scoped.Aliases = []int64{2, 3}
	scoped.SizeSort = "desc"
	expect(scoped, []int64{1}, 1)
	q.Excluded = map[int64]bool{2: true}
	expect(q, []int64{1}, 1)
	q.Start = 1
	expect(q, nil, 1)
	q.Start = 0
	q.Keyword = "雍正 大传"
	expect(q, []int64{1}, 1)
	q.Keyword = "雍正 OR 西游记"
	expect(q, []int64{3}, 2)
	q.Start = 1
	expect(q, []int64{1}, 2)
	if _, err := db.Exec(`UPDATE books SET title='西游 西游外传 游记' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	q.Keyword, q.Start = "西游记", 0
	expect(q, []int64{3}, 1)
	q.Keyword = "外传"
	expect(q, []int64{1}, 1)
	index()
	expect(q, []int64{1}, 1)
	q.Keyword = "西游记"
	expect(q, []int64{3}, 1)
	if _, err := db.Exec(`DELETE FROM books WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	expect(q, nil, 0)
	index()
	var remains int
	if err := db.QueryRow(`SELECT count(*) FROM book_title_grams WHERE book_id=3`).Scan(&remains); err != nil || remains != 0 {
		t.Fatal(remains, err)
	}
	condition, args := titleKeyword("雍正")
	rows, err := db.Query("EXPLAIN QUERY PLAN SELECT b.id FROM books b WHERE "+condition, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH book_title_grams USING PRIMARY KEY (gram=?)") {
			return
		}
	}
	t.Fatal("plan did not seek to the requested title grams")
}

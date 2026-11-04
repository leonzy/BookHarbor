package library

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityBrowseFoldsBeforePagingWithVisibleFilteredRepresentative(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "逻辑思考的100个关键", Authors: []string{"茂木健一郎"}, ISBN: "9787505750579"}
	ids := []int64{}
	for _, format := range []string{"EPUB", "MOBI", "AZW3", "EPUB"} {
		id, e := ImportBook(db, m)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		if e = AddFormat(db, id, format, "/source/"+format, 100, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := ReconcileBookIdentity(db, ids[0], false); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	cases := []struct {
		name  string
		q     BrowseQuery
		want  []int64
		total int
	}{
		{"all", BrowseQuery{GroupWorks: true, Size: 100}, []int64{ids[0], 3, 2, 1}, 4},
		{"second page has no repeated edition", BrowseQuery{GroupWorks: true, Start: 1, Size: 1}, []int64{3}, 4},
		{"private representative", BrowseQuery{GroupWorks: true, Excluded: map[int64]bool{ids[0]: true}, Size: 1}, []int64{ids[1]}, 4},
		{"only mobi matches", BrowseQuery{GroupWorks: true, Format: "MOBI", Size: 100}, []int64{ids[1]}, 1},
		{"subset outside representative", BrowseQuery{GroupWorks: true, Candidates: []int64{ids[2], ids[3]}, Size: 100}, []int64{ids[2]}, 1},
		{"empty", BrowseQuery{GroupWorks: true, Candidates: []int64{}, Size: 100}, []int64{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, e := BrowseBooks(ctx, db, tc.q)
			if e != nil || total != tc.total || !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, total, e)
			}
		})
	}
	// Legacy relevance order chooses the first visible match, not the smallest ID.
	got, e := CollapseIdentityIDs(db, []int64{ids[3], 3, ids[0], ids[2]})
	if e != nil || !reflect.DeepEqual(got, []int64{ids[3], 3}) {
		t.Fatal(got, e)
	}
	// Fast base counts subtract only visible duplicate members.
	if _, e = db.Exec(`CREATE TABLE IF NOT EXISTS book_unavailable_sources(book_id INTEGER PRIMARY KEY)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO book_unavailable_sources VALUES(?)`, ids[0]); e != nil {
		t.Fatal(e)
	}
	got, total, e := BrowseBooks(ctx, db, BrowseQuery{GroupWorks: true, Excluded: map[int64]bool{ids[1]: true}, Size: 100})
	if e != nil || total != 4 || got[0] != ids[2] {
		t.Fatal(got, total, e)
	}
	// Group-member lookup must start with the partial managed-work index.
	hidden, e := identityHiddenSQL(ctx, db, "1=1")
	if e != nil {
		t.Fatal(e)
	}
	rows, e := db.Query("EXPLAIN QUERY PLAN " + hidden)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var a, b, c int
		var detail string
		rows.Scan(&a, &b, &c, &detail)
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "book_identity_group") {
		t.Fatal(plan)
	}
	if !strings.Contains(plan, "MATERIALIZE matching") {
		t.Fatal("filter must be evaluated once", plan)
	}
}

func TestIdentityBrowseSizeUsesVisibleGroupFiles(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "同书", Authors: []string{"作者"}, ISBN: "9780306406157"}
	a, _ := ImportBook(db, m)
	b, _ := ImportBook(db, m)
	m.Title = "独立书"
	m.ISBN = ""
	c, _ := ImportBook(db, m)
	for _, v := range []struct{ id, size int64 }{{a, 10}, {b, 1000}, {c, 100}} {
		if e := AddFormat(db, v.id, "EPUB", "/source/test", v.size, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := ReconcileBookIdentity(db, a, false); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		excluded map[int64]bool
		want     []int64
	}{{nil, []int64{a, c}}, {map[int64]bool{b: true}, []int64{c, a}}} {
		ids, n, e := BrowseBooks(context.Background(), db, BrowseQuery{GroupWorks: true, Candidates: []int64{a, b, c}, Size: 10, SizeSort: "desc", Excluded: v.excluded})
		if e != nil || n != 2 || !reflect.DeepEqual(ids, v.want) {
			t.Fatal(ids, n, e)
		}
	}
}

func TestIdentityFormatRankPageKeepsFoldingAndPrivateFallback(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "逻辑思考", Authors: []string{"作者"}, ISBN: "9780306406157"}
	a, _ := ImportBook(db, m)
	b, _ := ImportBook(db, m)
	for _, id := range []int64{a, b} {
		if e := AddFormat(db, id, "EPUB", "/source/test", 100, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := ReconcileBookIdentity(db, a, false); e != nil {
		t.Fatal(e)
	}
	_, rawTotal, e := BrowseBooks(context.Background(), db, BrowseQuery{Format: "EPUB", Size: 100})
	if e != nil {
		t.Fatal(e)
	}
	for _, excluded := range []map[int64]bool{nil, {a: true}} {
		ids, total, e := BrowseBooks(context.Background(), db, BrowseQuery{GroupWorks: true, RatingSort: true, Format: "EPUB", Size: 100, Excluded: excluded})
		if e != nil || total != rawTotal-1 {
			t.Fatal(ids, total, e)
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		if excluded == nil && (!seen[a] || seen[b]) {
			t.Fatal(ids)
		}
		if excluded != nil && (seen[a] || !seen[b]) {
			t.Fatal(ids)
		}
	}
}

func TestIdentityDirectoryOnlyCollapsesMembersInSelectedDirectory(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "同书", Authors: []string{"作者"}, ISBN: "9780306406157"}
	a, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	b, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ReconcileBookIdentity(db, a, false); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`CREATE TABLE IF NOT EXISTS book_directory_members(node_id INTEGER,book_id INTEGER,direct INTEGER)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO book_directory_members(node_id,book_id,direct,nested) VALUES(11,?,1,0)`, b); e != nil {
		t.Fatal(e)
	}
	ids, n, e := BrowseBooks(context.Background(), db, BrowseQuery{GroupWorks: true, DirectoryNode: 11, Size: 10})
	if e != nil || n != 1 || !reflect.DeepEqual(ids, []int64{b}) {
		t.Fatal(ids, n, e)
	}
}

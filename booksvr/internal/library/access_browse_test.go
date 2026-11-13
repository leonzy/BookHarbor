package library

import (
	"context"
	"reflect"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessBrowsePermissionsBeforeFilterCountPageAndSize(t *testing.T) {
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
	for _, stmt := range []string{
		`INSERT INTO acl_root(id,path,token,ready) VALUES('root','/books','test',1)`,
		`INSERT INTO works(id,title_norm,author_norm,work_key,display_title,display_author,created_at,updated_at) VALUES(100,'test','author','acl-test','test','author',0,0)`,
		`UPDATE books SET work_id=100 WHERE id IN (1,2)`,
		`INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature,managed_work) VALUES(1,'','test','author','a',100),(2,'','test','author','b',100)`,
	} {
		if _, err = s.DB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	p := access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.Selected, Targets: []access.Target{{Root: "root", Directory: "/public", Recursive: true}}}}}}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, p)
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
	f := func(id, format, dir string, size int64) AccessFileSnapshot {
		return AccessFileSnapshot{ID: id, Format: format, Size: size, Version: 1, Facts: access.FileFacts{Root: "root", Directory: dir, Ready: true}}
	}
	books := []AccessBookSnapshot{
		{ID: 1, Title: "one", Files: []AccessFileSnapshot{f("one-epub", "EPUB", "/public", 10), f("one-mobi", "MOBI", "/secret", 100000)}},
		{ID: 2, Title: "two", Files: []AccessFileSnapshot{f("two", "EPUB", "/public/child", 20)}},
		{ID: 3, Title: "three", Files: []AccessFileSnapshot{f("three", "PDF", "/public-other", 30)}},
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, books); err != nil {
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
	visibleData, err := AccessBooksData(ctx, db, scope, []int64{1, 2, 3})
	if err != nil || len(visibleData) != 2 {
		t.Fatal("detail permission filter", visibleData, err)
	}
	for _, b := range visibleData {
		if b.Path != "" || !reflect.DeepEqual(b.Formats, []string{"EPUB"}) || b.FormatSizes["MOBI"] != 0 {
			t.Fatal("detail exposed hidden path/format/size", b)
		}
	}
	members, err := AccessIdentityCardMembers(ctx, db, scope, []int64{1})
	if err != nil || !reflect.DeepEqual(members[1], []int64{1, 2}) {
		t.Fatal("work membership", members, err)
	}
	formatPage, formatTotal, err := AccessCategoryPage(ctx, db, scope, "formats", "", 1, 50)
	if err != nil || formatTotal != 1 || len(formatPage) != 1 || formatPage[0].Name != "EPUB" || formatPage[0].Count != 2 {
		t.Fatal("format facet disclosed hidden sources", formatPage, formatTotal, err)
	}
	if _, err = db.Exec(`INSERT INTO tags(id,name) VALUES(999,'ACL test'); INSERT INTO books_tags_link(book,tag) VALUES(1,999),(3,999)`); err != nil {
		t.Fatal(err)
	}
	tags, tagTotal, err := AccessCategoryPage(ctx, db, scope, "tags", "acl test", 1, 50)
	if err != nil || tagTotal != 1 || len(tags) != 1 || tags[0].Count != 1 {
		t.Fatal("facet count included hidden book", tags, tagTotal, err)
	}
	tagCounts, e := AccessTagCounts(ctx, db, scope)
	if e != nil || tagCounts["ACL test"] != 1 {
		t.Fatal("navigation exposed hidden tag counts", tagCounts, e)
	}
	prefixTags, prefixTotal, e := AccessCategoryPage(ctx, db, scope, "tags", "", 1, 50, "acl")
	if e != nil || prefixTotal != 1 || len(prefixTags) != 1 || prefixTags[0].Name != "ACL test" {
		t.Fatal("category prefix group", prefixTags, prefixTotal, e)
	}
	if _, err = db.Exec(`INSERT OR REPLACE INTO books_tags_link(book,tag) VALUES(1,999)`); err != nil {
		t.Fatal(err)
	}
	tags, tagTotal, err = AccessCategoryPage(ctx, db, scope, "tags", "acl test", 1, 50)
	if err != nil || tagTotal != 1 || len(tags) != 1 || tags[0].Count != 1 {
		t.Fatal("outer conflict policy inflated facet count", tags, tagTotal, err)
	}
	if _, err = db.Exec(`DELETE FROM books_tags_link WHERE book=1 AND tag=999`); err != nil {
		t.Fatal(err)
	}
	tags, tagTotal, err = AccessCategoryPage(ctx, db, scope, "tags", "acl test", 1, 50)
	if err != nil || tagTotal != 0 || len(tags) != 0 {
		t.Fatal("removed visible facet still exposed hidden title", tags, tagTotal, err)
	}
	for _, tt := range []struct {
		name  string
		q     AccessBrowseQuery
		ids   []int64
		total int
	}{
		{"page1", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 1}}, []int64{2}, 2},
		{"page2", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 1, Start: 1}}, []int64{1}, 2},
		{"visible-size", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, SizeSort: "desc"}}, []int64{2, 1}, 2},
		{"hidden-format", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, Format: "MOBI"}}, []int64{}, 0},
		{"direct", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Directory: &access.Target{Root: "root", Directory: "/public"}}, []int64{1}, 1},
		{"physical direct", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, PhysicalDirectory: "/books/public"}, []int64{1}, 1},
		{"physical recursive", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, DirectoryRecursive: true}, PhysicalDirectory: "/books/public"}, []int64{2, 1}, 2},
		{"recursive", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Directory: &access.Target{Root: "root", Directory: "/public", Recursive: true}}, []int64{2, 1}, 2},
		{"author", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, Filters: map[string]string{"author": "刘"}}}, []int64{2, 1}, 2},
		{"no candidates", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, Candidates: []int64{3}}}, []int64{}, 0},
		{"search OR respects scope", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Search: "title:'三体' or title:'活着'"}, []int64{1}, 1},
		{"search hidden format", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Search: "format:MOBI"}, []int64{}, 0},
		{"alias remains scoped", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Search: "no-such-title", SearchAliasIDs: []int64{1, 3}}, []int64{1}, 1},
		{"search relevance remains scoped", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Search: "no-such-title", SearchAliasIDs: []int64{1, 3}, SearchRelevance: true}, []int64{1}, 1},
		{"search NOT hidden format", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10}, Search: "not format:MOBI"}, []int64{2, 1}, 2},
		{"group after directory", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, GroupWorks: true, SizeSort: "desc"}, Directory: &access.Target{Root: "root", Directory: "/public/child"}}, []int64{2}, 1},
		{"group card", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, GroupWorks: true, SizeSort: "desc"}}, []int64{1}, 1},
		{"group indexed default", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 10, GroupWorks: true}}, []int64{1}, 1},
		{"group indexed second page", AccessBrowseQuery{BrowseQuery: BrowseQuery{Size: 1, Start: 1, GroupWorks: true}}, []int64{}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ids, total, e := AccessBrowseBooks(ctx, db, scope, tt.q)
			if e != nil || total != tt.total || !reflect.DeepEqual(ids, tt.ids) {
				t.Fatalf("%v %d %v", ids, total, e)
			}
		})
	}
	dirs, err := s.AccessDirectoryChildrenPage(ctx, scope, "/books")
	if err != nil || len(dirs) != 1 || dirs[0].Path != "/books/public" || dirs[0].Count != 2 || !dirs[0].HasChildren {
		t.Fatal("directory exposed hidden path/count", dirs, err)
	}
	if _, err = db.Exec(`DELETE FROM acl_profile_book WHERE book_id=2`); err != nil {
		t.Fatal(err)
	}
	dirs, err = s.AccessDirectoryChildrenPage(ctx, scope, "/books")
	if err != nil || len(dirs) != 1 || dirs[0].Count != 1 || dirs[0].HasChildren {
		t.Fatal("directory revoke did not update counters", dirs, err)
	}
}

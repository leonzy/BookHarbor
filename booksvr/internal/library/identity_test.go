package library

import (
	"context"
	"reflect"
	"talebook/booksvr/internal/bookidentity"
	"testing"
)

func TestIdentityGroupsVersionsPreservingFilesAndMetadata(t *testing.T) {
	db := newQueryDB(t)
	first := BookMeta{Title: "AI未来进行式", Authors: []string{"李开复", "陈楸帆"}, ISBN: "9787213101625", Path: "/source/one"}
	a, e := ImportBook(db, first)
	if e != nil {
		t.Fatal(e)
	}
	second := first
	second.Title = "AI未来进行式（李开复陈楸帆新书，樊登、刘慈欣等强势推荐！） (财之道丛书)"
	second.Authors = []string{"李开复"}
	b, e := ImportBook(db, second)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ImportBook(db, first)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		id           int64
		format, path string
	}{{a, "EPUB", "/one.epub"}, {b, "MOBI", "/two.mobi"}, {c, "EPUB", "/better.epub"}} {
		if e = AddFormat(db, v.id, v.format, v.path, 123, 1); e != nil {
			t.Fatal(e)
		}
	}
	preview, e := ReconcileBookIdentity(db, a, true)
	if e != nil || len(preview.Members) != 3 {
		t.Fatal(preview, e)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM book_identity_keys`).Scan(&n)
	if n != 0 {
		t.Fatal("dry run wrote")
	}
	result, e := ReconcileBookIdentity(db, a, false)
	if e != nil || len(result.Members) != 3 {
		t.Fatal(result, e)
	}
	ids, e := IdentityVersions(db, a)
	if e != nil || !reflect.DeepEqual(ids, []int64{b, c}) {
		t.Fatal(ids, e)
	}
	var title string
	db.QueryRow(`SELECT title FROM books WHERE id=?`, b).Scan(&title)
	if title != second.Title {
		t.Fatal("metadata overwritten")
	}
	db.QueryRow(`SELECT count(*) FROM book_formats WHERE book_id IN (?,?,?)`, a, b, c).Scan(&n)
	if n != 3 {
		t.Fatal("files lost")
	}
	again, e := ReconcileBookIdentity(db, b, false)
	if e != nil || again.WorkID != result.WorkID {
		t.Fatal(again, e)
	}
	if _, e = db.Exec(`UPDATE books SET title='完全不同的作品' WHERE id=?`, b); e != nil {
		t.Fatal(e)
	}
	changed, e := ReconcileBookIdentity(db, b, false)
	if e != nil || changed.WorkID == result.WorkID {
		t.Fatal("stale group retained", changed, e)
	}
}
func TestIdentityQueueCapturesOldWriterAndPreservesManualGroup(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "测试作品", Authors: []string{"作者"}, ISBN: "9780306406157"}
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
	db.Exec(`DELETE FROM book_identity_queue`)
	// Simulate an older metadata binary: it knows nothing about identity jobs.
	db.Exec(`UPDATE identifiers SET val='0-306-40615-2' WHERE book=? AND type='isbn'`, b)
	var n int
	db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id=?`, b).Scan(&n)
	if n != 1 {
		t.Fatal("old writer event lost")
	}
	if _, e = RunIdentityBatch(context.Background(), db, "test-worker", 10); e != nil {
		t.Fatal(e)
	}
	split, e := SplitBook(db, b)
	if e != nil {
		t.Fatal(e)
	}
	result, e := ReconcileBookIdentity(db, b, false)
	if e != nil || result.Reason != "manual_group" || result.WorkID != split {
		t.Fatal(result, e)
	}
	db.Exec(`UPDATE identifiers SET val='9780306406157' WHERE book=?`, b)
	if _, e = RunIdentityBatch(context.Background(), db, "test-worker-restarted", 10); e != nil {
		t.Fatal(e)
	}
	work, e := GetBookWorkID(db, b)
	if e != nil || work != split {
		t.Fatal("manual split undone", work, e)
	}
}
func TestIdentityMatcherRejectsISBNConflictsAndAuthorBridges(t *testing.T) {
	db := newQueryDB(t)
	base := bookidentity.Metadata{Title: "实验作品", ISBN: "9780306406157", Authors: []string{"甲"}}
	for _, authors := range [][]string{{"甲", "乙"}, {"甲", "丙"}} {
		if _, e := ImportBook(db, BookMeta{Title: base.Title, Authors: authors, ISBN: base.ISBN}); e != nil {
			t.Fatal(e)
		}
	}
	ids, e := MatchIdentityBooks(db, base)
	if e != nil || len(ids) != 0 {
		t.Fatal("partial author bridged incompatible groups", ids, e)
	}
}

func TestIdentityRegistrationRechecksMetadataBeforeAddingFormat(t *testing.T) {
	db := newQueryDB(t)
	m := bookidentity.Metadata{Title: "初始书名", Authors: []string{"作者"}, ISBN: "9780306406157"}
	id, e := ImportBook(db, BookMeta{Title: m.Title, Authors: m.Authors, ISBN: m.ISBN})
	if e != nil {
		t.Fatal(e)
	}
	ids, e := MatchIdentityBooks(db, m)
	if e != nil || len(ids) != 1 {
		t.Fatal(ids, e)
	}
	db.Exec(`UPDATE books SET title='已经改成另一本' WHERE id=?`, id)
	if e = AddIdentityFormat(db, id, m, "EPUB", "/source.epub", 100, 0); e != ErrIdentityChanged {
		t.Fatal(e)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM book_formats WHERE book_id=?`, id).Scan(&count)
	if count != 0 {
		t.Fatal("stale match added file")
	}
}

func TestIdentityStrongISBNIsNotDrownedOutByCommonTitle(t *testing.T) {
	db := newQueryDB(t)
	for i := 0; i < 70; i++ {
		if _, e := db.Exec(`INSERT INTO books(title,author_sort,path) VALUES('常见书名','其他作者','')`); e != nil {
			t.Fatal(e)
		}
	}
	m := BookMeta{Title: "常见书名", Authors: []string{"真实作者"}, ISBN: "9780306406157"}
	id, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	ids, e := MatchIdentityBooks(db, bookidentity.Metadata{Title: m.Title, Authors: m.Authors, ISBN: m.ISBN})
	if e != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal(ids, e)
	}
}

func TestIdentityBatchKeepsNewerEventsAndHonorsLease(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "队列作品", Authors: []string{"作者"}, ISBN: "9780306406157"}
	a, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ImportBook(db, m); e != nil {
		t.Fatal(e)
	}
	db.Exec(`INSERT INTO book_identity_lease(id,owner,expires) VALUES(1,'other',unixepoch()+60)`)
	if n, e := RunIdentityBatch(context.Background(), db, "this", 1); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	db.Exec(`DELETE FROM book_identity_lease`)
	// Emulate a newer metadata write after the worker captured its queue version.
	db.Exec(`CREATE TRIGGER test_identity_new_event AFTER UPDATE OF work_id ON books BEGIN UPDATE books SET title=title WHERE id=NEW.id; END`)
	if _, e = RunIdentityBatch(context.Background(), db, "this", 10); e != nil {
		t.Fatal(e)
	}
	var pending bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM book_identity_queue WHERE book_id=?)`, a).Scan(&pending)
	if !pending {
		t.Fatal("newer event was acknowledged")
	}
	db.Exec(`DROP TRIGGER test_identity_new_event`)
	if _, e = RunIdentityBatch(context.Background(), db, "restarted", 10); e != nil {
		t.Fatal(e)
	}
}

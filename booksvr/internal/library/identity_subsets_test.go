package library

import (
	"context"
	"fmt"
	"reflect"
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestNewProjectionVersionRechecksMarketingAndBilingualCredits(t *testing.T) {
	db := newQueryDB(t)
	title := "从1到N：企业数字化生存指南"
	a, err := ImportBook(db, BookMeta{Title: title, Authors: []string{"【德】尤尔根·梅菲特（Jürgen Meffert） 沙莎"}, ISBN: "9787313191304"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ImportBook(db, BookMeta{Title: title + "（来自麦肯锡团队的企业数字化指南）", Authors: []string{"尤尔根·梅菲特", "沙莎"}, ISBN: "9787313191304"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if _, err := ReconcileBookIdentity(db, id, false); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`UPDATE books SET work_id=NULL`)
	db.Exec(`UPDATE book_identity_keys SET managed_work=0,author_key='old combined author',title_key='old promotion',reason='conflicting_candidates'`)
	db.Exec(`DELETE FROM book_identity_queue`)
	db.Exec(`INSERT INTO book_identity_state(key,value) VALUES('authors_v3_done',1)`)
	for i := 0; i < 4; i++ {
		if _, err := RunIdentityBatch(context.Background(), db, "v4-recheck", 10); err != nil {
			t.Fatal(err)
		}
	}
	var x, y int64
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, a).Scan(&x)
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, b).Scan(&y)
	if x == 0 || x != y {
		t.Fatal("new projection did not reconcile", x, y)
	}
	var raw string
	db.QueryRow(`SELECT title FROM books WHERE id=?`, b).Scan(&raw)
	if raw == title {
		t.Fatal("raw title overwritten")
	}
	var done int
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='authors_v5_done'`).Scan(&done)
	if done != 1 {
		t.Fatal("new cursor did not finish")
	}
}

func TestNeighborhoodHasReadBudgetForRejectedISBNDistractors(t *testing.T) {
	db := newQueryDB(t)
	isbns := []string{"9787313191304", "9780306406157", "9787514372793", "9787550266599"}
	var root int64
	for _, isbn := range isbns {
		id, err := ImportBook(db, BookMeta{Title: "有界邻域", Authors: []string{"甲"}, ISBN: isbn})
		if err != nil {
			t.Fatal(err)
		}
		root = id
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i, isbn := range isbns {
		for j := 0; j < 40; j++ {
			r, err := tx.Exec(`INSERT INTO books(title,author_sort,path) VALUES(?,'乙','')`, fmt.Sprintf("不同题名-%d-%d", i, j))
			if err != nil {
				t.Fatal(err)
			}
			id, err := r.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`INSERT INTO identifiers(book,type,val) VALUES(?,'isbn',?)`, id, isbn); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r, err := ReconcileBookIdentity(db, root, true)
	if err != nil || r.Reason != "too_many_candidates" {
		t.Fatal("unbounded rejected-candidate reads", r, err)
	}
}

func TestNeighborhoodEntryAndProcessingOrderCannotClaimAmbiguousBridge(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 0, 3, 2}} {
		db := newQueryDB(t)
		var ids []int64
		for _, authors := range [][]string{{"甲"}, {"甲", "乙"}, {"甲", "丙"}, {"甲", "乙"}} {
			id, err := ImportBook(db, BookMeta{Title: "歧义候选作品", Authors: authors, ISBN: "9787313191304"})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		for _, i := range order {
			if _, err := ReconcileBookIdentity(db, ids[i], false); err != nil {
				t.Fatal(err)
			}
		}
		var works [4]int64
		for i, id := range ids {
			if err := db.QueryRow(`SELECT coalesce(work_id,0) FROM books WHERE id=?`, id).Scan(&works[i]); err != nil {
				t.Fatal(err)
			}
		}
		if works[0] != 0 || works[1] == 0 || works[1] != works[3] || works[2] == works[1] {
			t.Fatal("order-dependent bridge", order, works)
		}
	}
}

func TestExistingAmbiguousUnitRepairRequeuesPreviouslyBlockedNeighbors(t *testing.T) {
	db := newQueryDB(t)
	create := func(authors []string) int64 {
		id, err := ImportBook(db, BookMeta{Title: "旧组处理顺序", Authors: authors, ISBN: "9787313191304"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := create([]string{"甲"})
	ac := create([]string{"甲", "丙"})
	if _, err := ReconcileBookIdentity(db, ac, false); err != nil {
		t.Fatal(err)
	}
	ab := create([]string{"甲", "乙"})
	ab2 := create([]string{"甲", "乙"})
	ac2 := create([]string{"甲", "丙"})
	db.Exec(`DELETE FROM book_identity_queue`)
	db.Exec(`INSERT OR REPLACE INTO book_identity_state(key,value) VALUES('cursor',?),('authors_v5_done',1)`, ac2)
	for priority, id := range []int64{ab2, ac2, ab, ac, a} {
		if _, err := db.Exec(`INSERT INTO book_identity_queue(book_id,priority) VALUES(?,?)`, id, 100-priority); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		if _, err := RunIdentityBatch(context.Background(), db, "old-unit-repair", 1); err != nil {
			t.Fatal(err)
		}
	}
	work := func(id int64) int64 {
		var v int64
		if err := db.QueryRow(`SELECT coalesce(work_id,0) FROM books WHERE id=?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if work(a) != 0 || work(ab) == 0 || work(ab) != work(ab2) || work(ac) == 0 || work(ac) != work(ac2) || work(ab) == work(ac) {
		t.Fatal("blocked neighbors never revisited", work(a), work(ab), work(ab2), work(ac), work(ac2))
	}
}

func TestFullMissingISBNWitnessIsSymmetricAcrossEntries(t *testing.T) {
	db := newQueryDB(t)
	var ids []int64
	for i, authors := range [][]string{{"尤尔根·梅菲特", "沙莎"}, {"尤尔根·梅菲特"}, {"尤尔根·梅菲特", "沙莎"}} {
		isbn := "9787313191304"
		if i == 2 {
			isbn = ""
		}
		id, err := ImportBook(db, BookMeta{Title: "从1到N：企业数字化生存指南", Authors: authors, ISBN: isbn})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		r, err := ReconcileBookIdentity(db, id, true)
		if err != nil || !reflect.DeepEqual(r.Members, ids) {
			t.Fatal(id, r, err)
		}
	}
}

func TestFileCoauthorsAppendOnlyWithCompleteSourceEvidenceAndNoManualLock(t *testing.T) {
	for _, mode := range []string{"file", "network", "authors-lock", "sort-lock", "conflict"} {
		db := newQueryDB(t)
		id, err := ImportBook(db, BookMeta{Title: "来源佐证", Authors: []string{"【德】尤尔根·梅菲特"}})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "authors-lock" || mode == "sort-lock" {
			field := "authors"
			if mode == "sort-lock" {
				field = "author_sort"
			}
			if _, err = db.Exec(`INSERT INTO metadata_field_locks(book_id,field) VALUES(?,?)`, id, field); err != nil {
				t.Fatal(err)
			}
		}
		origin := "file"
		if mode == "network" {
			origin = "network:test"
		}
		authors := []string{"尤尔根·梅菲特", "沙莎"}
		if mode == "conflict" {
			authors = []string{"张三", "沙莎"}
		}
		_, err = FillMissingMetadata(db, id, importmeta.Metadata{Authors: authors}, origin)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err = db.QueryRow(`SELECT count(*) FROM books_authors_link WHERE book=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 1
		if mode == "file" {
			want = 2
		}
		if n != want {
			t.Fatal(mode, n, want)
		}
	}
}

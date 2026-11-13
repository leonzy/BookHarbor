package library

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnonymousCompanionsGroupAndPathChangesRequeue(t *testing.T) {
	db := newQueryDB(t)
	var ids []int64
	for i, format := range []string{"EPUB", "MOBI", "EPUB"} {
		id, err := ImportBook(db, BookMeta{Title: "大国重工", Authors: []string{"佚名"}})
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join("/books", "目录", "Q047-大国重工 齐橙")
		if i == 2 {
			name = filepath.Join("/other", "Q047-大国重工 齐橙")
		}
		if err = AddFormat(db, id, format, name+"."+format, 20000, 1); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, err := ReconcileBookIdentity(db, id, false); err != nil {
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
	if work(ids[0]) == 0 || work(ids[0]) != work(ids[1]) || work(ids[2]) != 0 {
		t.Fatal("companions not separated from namesake")
	}
	var before, after int
	db.QueryRow(`SELECT version FROM book_identity_queue WHERE book_id=?`, ids[1]).Scan(&before)
	if _, err := db.Exec(`UPDATE book_formats SET path=? WHERE book_id=?`, filepath.Join("/moved", "other.mobi"), ids[1]); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT version FROM book_identity_queue WHERE book_id=?`, ids[1]).Scan(&after)
	if after <= before {
		t.Fatal("path update did not version event", before, after)
	}
	for i := 0; i < 8; i++ {
		if _, err := RunIdentityBatch(context.Background(), db, "source-recheck", 10); err != nil {
			t.Fatal(err)
		}
	}
	if work(ids[1]) != 0 {
		t.Fatal("stale source association retained")
	}
	var author string
	db.QueryRow(`SELECT author_sort FROM books WHERE id=?`, ids[0]).Scan(&author)
	if author != "佚名" {
		t.Fatal("source fallback guessed author", author)
	}
}

func TestAnonymousSourceDiscoveryQueuesOnlyIndexedCompanions(t *testing.T) {
	db := newQueryDB(t)
	var ids []int64
	for i, format := range []string{"EPUB", "MOBI", "PDF", "TXT"} {
		id, err := ImportBook(db, BookMeta{Title: "同名候选"})
		if err != nil {
			t.Fatal(err)
		}
		stem := filepath.Join("/books", "real")
		if i > 1 {
			stem = filepath.Join("/other", format)
		}
		if err = AddFormat(db, id, format, stem+"."+strings.ToLower(format), 20000, 1); err != nil {
			t.Fatal(err)
		}
		if _, err = ReconcileBookIdentity(db, id, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	db.Exec(`DELETE FROM book_identity_queue`)
	db.Exec(`INSERT OR REPLACE INTO book_identity_state(key,value) VALUES('authors_v5_done',1)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = refreshIdentitySources(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count, invalid int
	db.QueryRow(`SELECT count(*) FROM book_identity_queue`).Scan(&count)
	db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id NOT IN (?,?)`, ids[0], ids[1]).Scan(&invalid)
	if count != 2 || invalid != 0 {
		t.Fatal("unrelated namesakes were enqueued", count, invalid)
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT book_id,format,path FROM book_formats INDEXED BY source_sync_format_path WHERE path>=? AND path<? ORDER BY path LIMIT 65`, "/books/real.", "/books/real/")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail
	}
	if err = rows.Err(); err != nil || !strings.Contains(plan, "SEARCH book_formats USING INDEX source_sync_format_path") {
		t.Fatal("companion discovery did not use indexed range", plan, err)
	}
}

func TestSourceEvidenceRejectsArchiveAndWrongExtension(t *testing.T) {
	db := newQueryDB(t)
	id, err := ImportBook(db, BookMeta{Title: "来源边界"})
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"talebook-zip:v1:not-a-local-path.epub", "/books/not-epub.zip"} {
		if _, err := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(?,?,?,1,1)`, id, []string{"EPUB", "MOBI"}[i], path); err != nil {
			t.Fatal(err)
		}
	}
	if keys, err := identitySourceFamilies(db, id); err != nil || len(keys) != 0 {
		t.Fatal(keys, err)
	}
}

func TestSourceEvidenceBoundAndManualProtection(t *testing.T) {
	db := newQueryDB(t)
	a, _ := ImportBook(db, BookMeta{Title: "受保护配套"})
	b, _ := ImportBook(db, BookMeta{Title: "受保护配套"})
	for i, id := range []int64{a, b} {
		format := []string{"EPUB", "MOBI"}[i]
		if err := AddFormat(db, id, format, filepath.Join("/books", "配套.")+format, 20000, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO book_identity_locks(book_id) VALUES(?)`, b); err != nil {
		t.Fatal(err)
	}
	r, err := ReconcileBookIdentity(db, a, false)
	if err != nil || len(r.Members) != 1 {
		t.Fatal("manual sibling absorbed", r, err)
	}
	for i := 0; i < 17; i++ {
		if _, err := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(?,?,?,1,1)`, a, string(rune('A'+i)), "/test/path."+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if keys, err := identitySourceFamilies(db, a); err != nil || len(keys) != 0 {
		t.Fatal("source evidence exceeded budget", keys, err)
	}
}

func TestAnonymousSourceHistoryUsesIndependentResumableCursor(t *testing.T) {
	db := newQueryDB(t)
	a, _ := ImportBook(db, BookMeta{Title: "历史无作者配套"})
	b, _ := ImportBook(db, BookMeta{Title: "历史无作者配套"})
	for i, id := range []int64{a, b} {
		format := []string{"EPUB", "MOBI"}[i]
		if err := AddFormat(db, id, format, filepath.Join("/books", "历史.")+format, 20000, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconcileBookIdentity(db, id, false); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`UPDATE books SET work_id=NULL`)
	db.Exec(`UPDATE book_identity_keys SET managed_work=0,reason='single'`)
	db.Exec(`DELETE FROM book_identity_queue`)
	db.Exec(`INSERT OR REPLACE INTO book_identity_state(key,value) VALUES('authors_v5_done',1),('authors_v5_cursor',77),('cursor',?)`, b)
	for i := 0; i < 4; i++ {
		if _, err := RunIdentityBatch(context.Background(), db, "sources-history", 10); err != nil {
			t.Fatal(err)
		}
	}
	var x, y, seed, authors, done int64
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, a).Scan(&x)
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, b).Scan(&y)
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='cursor'`).Scan(&seed)
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='authors_v5_cursor'`).Scan(&authors)
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='sources_v2_done'`).Scan(&done)
	if x == 0 || x != y || seed != b || authors != 77 || done != 1 {
		t.Fatal(x, y, seed, authors, done)
	}
}

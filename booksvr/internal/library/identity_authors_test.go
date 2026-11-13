package library

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHistoricalAuthorRepairResumesAndKeepsMetadataAndManualGroups(t *testing.T) {
	db := newQueryDB(t)
	a, e := ImportBook(db, BookMeta{Title: "雍正大传", Authors: []string{"关河五十州"}, ISBN: "9787514372793"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := ImportBook(db, BookMeta{Title: "雍正大传", Authors: []string{"关河五十州, ePUBw.COM"}, ISBN: "9787514372793"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ReconcileBookIdentity(db, a, false); e != nil {
		t.Fatal(e)
	}
	// Simulate the completed old projection and empty queue from production.
	db.Exec(`UPDATE books SET work_id=NULL`)
	db.Exec(`UPDATE book_identity_keys SET managed_work=0,author_key='old polluted key' WHERE book_id=?`, b)
	db.Exec(`DELETE FROM book_identity_queue`)
	for i := 0; i < 3; i++ {
		if _, e = RunIdentityBatch(context.Background(), db, "restarted-worker", 10); e != nil {
			t.Fatal(e)
		}
	}
	var wa, wb int64
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, a).Scan(&wa)
	db.QueryRow(`SELECT work_id FROM books WHERE id=?`, b).Scan(&wb)
	if wa == 0 || wa != wb {
		t.Fatal("historical pair remains split", wa, wb)
	}
	var raw string
	db.QueryRow(`SELECT author_sort FROM books WHERE id=?`, b).Scan(&raw)
	if raw != "关河五十州, ePUBw.COM" {
		t.Fatal("raw metadata edited", raw)
	}
	var done int
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='authors_v3_done'`).Scan(&done)
	if done != 1 {
		t.Fatal("discovery not checkpointed")
	}
	if _, e = SplitBook(db, b); e != nil {
		t.Fatal(e)
	}
	db.Exec(`DELETE FROM book_identity_state WHERE key LIKE 'authors_v3_%'`)
	for i := 0; i < 3; i++ {
		if _, e = RunIdentityBatch(context.Background(), db, "second-pass", 10); e != nil {
			t.Fatal(e)
		}
	}
	db.QueryRow(`SELECT coalesce(work_id,0) FROM books WHERE id=?`, b).Scan(&wb)
	if wb == wa {
		t.Fatal("manual split overwritten")
	}
}

func TestIdentityAuthorRepairWithConcurrentMetadataWriter(t *testing.T) {
	db := newQueryDB(t)
	db.SetMaxOpenConns(1)
	if err := EnsureIdentitySchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;PRAGMA busy_timeout=3000`); err != nil {
		t.Fatal(err)
	}
	for id := 4; id <= 204; id++ {
		if _, err := db.Exec(`INSERT INTO books(id,title,author_sort,path) VALUES(?,?,?,?)`, id, fmt.Sprint("并发样本", id), "张三, epubw.com", "/source"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature) VALUES(?,'',?,'old','old')`, id, fmt.Sprint("并发样本", id)); err != nil {
			t.Fatal(err)
		}
	}
	var seq int
	var name, file string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", file+"?_pragma=busy_timeout(3000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	stop, finished, ready := make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() {
		first := true
		for {
			select {
			case <-stop:
				finished <- nil
				return
			default:
			}
			_, e := other.Exec(`UPDATE books SET timestamp=timestamp+1 WHERE id=1`)
			if first {
				close(ready)
				first = false
			}
			if e != nil && !strings.Contains(e.Error(), "SQLITE_BUSY") {
				finished <- e
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	<-ready
	for i := 0; i < 4; i++ {
		if _, e := RunIdentityBatch(context.Background(), db, "concurrent-repair", 1); e != nil {
			close(stop)
			<-finished
			t.Fatal("writer snapshot upgrade failed", e)
		}
	}
	close(stop)
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	var done int
	db.QueryRow(`SELECT value FROM book_identity_state WHERE key='authors_v3_done'`).Scan(&done)
	if done != 1 {
		t.Fatal("cursor failed under concurrent writes")
	}
}

func TestIdentityGroupAcknowledgementPreservesLaterEventsAndRestores(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint(restore), func(t *testing.T) {
			db := newQueryDB(t)
			m := BookMeta{Title: "队列测试", Authors: []string{"作者甲"}, ISBN: "9780306406157"}
			a, e := ImportBook(db, m)
			if e != nil {
				t.Fatal(e)
			}
			b, e := ImportBook(db, m)
			if e != nil {
				t.Fatal(e)
			}
			r, e := ReconcileBookIdentity(db, a, false)
			if e != nil || len(r.ack) != 2 {
				t.Fatal(r, e)
			}
			if restore {
				if _, e = db.Exec(`DELETE FROM books WHERE id=?`, b); e != nil {
					t.Fatal(e)
				}
				if _, e = db.Exec(`INSERT INTO books(id,title,author_sort,path) VALUES(?,'恢复的新记录','作者乙','/restored')`, b); e != nil {
					t.Fatal(e)
				}
				// Reusing a queue generation cannot authorize an old group's acknowledgement.
				db.Exec(`UPDATE book_identity_queue SET version=? WHERE book_id=?`, r.ack[b].version, b)
			} else {
				if _, e = db.Exec(`UPDATE books SET title='用户刚刚改过的书名' WHERE id=?`, b); e != nil {
					t.Fatal(e)
				}
			}
			if e = acknowledgeIdentity(db, a, r.ack[a].version, r); e != nil {
				t.Fatal(e)
			}
			var n int
			db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id=?`, b).Scan(&n)
			if n != 1 {
				t.Fatal("new event lost")
			}
			db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id=?`, a).Scan(&n)
			if n != 0 {
				t.Fatal("completed member not acknowledged")
			}
		})
	}
}

func TestIdentityBatchAcknowledgesSiblingsAndSparseUniqueRekey(t *testing.T) {
	db := newQueryDB(t)
	m := BookMeta{Title: "同组测试", Authors: []string{"作者甲"}, ISBN: "9780306406157"}
	a, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	b, e := ImportBook(db, m)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := RunIdentityBatch(context.Background(), db, "one-root", 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id IN (?,?)`, a, b).Scan(&count)
	if count != 0 {
		t.Fatal("siblings queued for redundant recomputation", count)
	}
	u, e := ImportBook(db, BookMeta{Title: "独立作品", Authors: []string{"张三, epubw.com"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ReconcileBookIdentity(db, u, false); e != nil {
		t.Fatal(e)
	}
	db.Exec(`UPDATE book_identity_keys SET author_key='old polluted key' WHERE book_id=?`, u)
	db.Exec(`DELETE FROM book_identity_queue`)
	db.Exec(`DELETE FROM book_identity_state WHERE key LIKE 'authors_v3_%'`)
	db.Exec(`INSERT INTO book_identity_state(key,value) VALUES('cursor',999999) ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
	if _, e = RunIdentityBatch(context.Background(), db, "sparse", 10); e != nil {
		t.Fatal(e)
	}
	var key string
	db.QueryRow(`SELECT author_key FROM book_identity_keys WHERE book_id=?`, u).Scan(&key)
	if key != "张三" {
		t.Fatal(key)
	}
	db.QueryRow(`SELECT count(*) FROM book_identity_queue WHERE book_id=?`, u).Scan(&count)
	if count != 0 {
		t.Fatal("unique unchanged identity unnecessarily queued")
	}
}

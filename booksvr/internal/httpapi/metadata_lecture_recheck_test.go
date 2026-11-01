package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
	"testing"
	"time"
)

func lectureFixture(t *testing.T) (*metadataWorker, string) {
	t.Helper()
	w, _, old := referenceFixture(t)
	if err := library.Migrate(w.libraryDB); err != nil {
		t.Fatal(err)
	}
	name := "030919存在与人生-何光沪"
	file := filepath.Join(filepath.Dir(old), name+".txt")
	if err := os.WriteFile(file, []byte("主讲人简介\n何光沪，教授。\n内容简介\n存在与人生。\n全文\n讲座。"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.libraryDB.Exec(`UPDATE book_formats SET path=? WHERE book_id=1`, file); err != nil {
		t.Fatal(err)
	}
	if _, err := w.libraryDB.Exec(`UPDATE books SET title=?,sort=? WHERE id=1`, name, name); err != nil {
		t.Fatal(err)
	}
	return testMetadataWorker(t, w), file
}

func lectureCount(t *testing.T, m *metadataWorker, status string) int {
	t.Helper()
	var n int
	if err := m.w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_lecture_rechecks WHERE status=?`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLectureRecheckDiscoveryRepairAndResume(t *testing.T) {
	m, file := lectureFixture(t)
	// Preserve the ordinary worker's historical cursor and active source phase.
	if err := m.setState("cursor", 42); err != nil {
		t.Fatal(err)
	}
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "pending") != 1 {
		t.Fatal("candidate not queued")
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "repaired") != 1 {
		t.Fatal("not repaired")
	}
	b, err := library.GetBooksData(m.w.libraryDB, []int64{1})
	if err != nil || b[0].Title != "存在与人生" || b[0].Authors[0] != "何光沪" {
		t.Fatal(b, err)
	}
	if jobStatus(t, m, 1) != "network_pending" {
		t.Fatal("identity not requeued")
	}
	if cursor, err := m.state("cursor"); err != nil || cursor != 42 {
		t.Fatal(cursor, err)
	}
	body, err := os.ReadFile(file)
	if err != nil || len(body) == 0 {
		t.Fatal(err)
	}
	// A new worker resumes the persistent ledger without reprocessing a repaired book.
	m2 := testMetadataWorker(t, m.w)
	if err = m2.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	if err = m2.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m2, "repaired") != 1 {
		t.Fatal("lost checkpoint")
	}
	s, err := lectureRecheckStatus(m.w.libraryDB)
	if err != nil || s["total"] != int64(1) || s["checked"] != int64(1) || s["discovery_done"] != true {
		t.Fatal(s, err)
	}
}

func TestLectureRecheckPreservesManualAndConflictingInformation(t *testing.T) {
	for _, kind := range []string{"manual", "custom-title", "known-author", "no-evidence", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			m, file := lectureFixture(t)
			if err := m.discoverLectureRechecks(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "manual":
				if _, err := m.w.libraryDB.Exec(`INSERT INTO metadata_field_locks(book_id,field) VALUES(1,'title')`); err != nil {
					t.Fatal(err)
				}
			case "custom-title":
				m.w.libraryDB.Exec(`UPDATE books SET title='人工书名' WHERE id=1`)
			case "known-author":
				m.w.libraryDB.Exec(`INSERT INTO authors(id,name,sort) VALUES(999,'不同作者','不同作者')`)
				m.w.libraryDB.Exec(`DELETE FROM books_authors_link WHERE book=1`)
				m.w.libraryDB.Exec(`INSERT INTO books_authors_link(book,author) VALUES(1,999)`)
				m.w.libraryDB.Exec(`UPDATE books SET author_sort='不同作者' WHERE id=1`)
			case "no-evidence":
				os.WriteFile(file, []byte("普通正文，无资料"), 0600)
			case "unreadable":
				os.Remove(file)
			}
			before, err := library.MetadataIdentity(m.w.libraryDB, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.recheckLectureOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, err := library.MetadataIdentity(m.w.libraryDB, 1)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range before {
				if after[key] != value {
					t.Fatal("overwrote", key)
				}
			}
			status := map[string]string{"manual": "protected", "custom-title": "conflict", "known-author": "conflict", "no-evidence": "no_evidence", "unreadable": "unreadable"}[kind]
			if lectureCount(t, m, status) != 1 {
				t.Fatal("unexpected status", status)
			}
			if kind == "unreadable" {
				var at int64
				m.w.libraryDB.QueryRow(`SELECT next_at FROM metadata_lecture_rechecks WHERE book_id=1`).Scan(&at)
				if at != m.now().Add(time.Hour).Unix() {
					t.Fatal(at)
				}
			}
		})
	}
}

func TestLectureRecheckKeepsActiveSourcesAndCorrectBookJobs(t *testing.T) {
	m, _ := lectureFixture(t)
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,record,next_at) VALUES(1,81,'detail','{"title":"held"}',1800000009)`,
		`INSERT INTO metadata_source_jobs(book_id,connection_id,phase) VALUES(1,82,'done')`,
	} {
		if _, err := m.w.libraryDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var phase, record string
	var at int64
	if err := m.w.libraryDB.QueryRow(`SELECT phase,record,next_at FROM metadata_source_jobs WHERE connection_id=81`).Scan(&phase, &record, &at); err != nil {
		t.Fatal(err)
	}
	if phase != "detail" || record != `{"title":"held"}` || at != 1800000009 {
		t.Fatal("active source reset", phase, record, at)
	}
	var n int
	m.w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs WHERE connection_id=82`).Scan(&n)
	if n != 0 {
		t.Fatal("stale completed identity not invalidated")
	}
	// Force a second verification, not a second metadata job reset.
	m.w.libraryDB.Exec(`UPDATE metadata_lecture_rechecks SET status='pending',created_at=1800000001,next_at=0`)
	if err := m.saveJob(1, "partial", "preserve reason", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "correct") != 1 || jobStatus(t, m, 1) != "partial" {
		t.Fatal("correct book job reset")
	}
}

func TestLectureRecheckFixedSnapshotAndOneBookBudget(t *testing.T) {
	m, _ := lectureFixture(t)
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	// Records beyond the discovery high-water mark belong to normal new-import work.
	insertBook(t, m.w.libraryDB, 2, "新导入")
	if _, err := m.w.libraryDB.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(2,'TXT','/new/030919存在与人生-何光沪.txt',1,0)`); err != nil {
		t.Fatal(err)
	}
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "pending") != 1 {
		t.Fatal("expanded fixed snapshot")
	}
	if _, err := m.w.libraryDB.Exec(`INSERT INTO metadata_lecture_rechecks(book_id,source_name) VALUES(999,'030919存在与人生-何光沪')`); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "pending") != 1 {
		t.Fatal("more than one book processed")
	}
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "pending") != 1 {
		t.Fatal("discovery repeated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.recheckLectureOne(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if lectureCount(t, m, "pending") != 1 {
		t.Fatal("cancel mutated queue")
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "deleted") != 1 {
		t.Fatal("deleted book not distinguished")
	}
}

func TestLectureRecheckStaleIdentityDoesNotResetJobs(t *testing.T) {
	m, _ := lectureFixture(t)
	expected, err := library.MetadataIdentity(m.w.libraryDB, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.w.libraryDB.Exec(`UPDATE books SET title='刚刚人工保存' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = m.saveJob(1, "partial", "keep job", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err = m.applyLocalMetadata(1, importmeta.Metadata{Title: "存在与人生", Authors: []string{"何光沪"}}, "partial", expected); err != nil {
		t.Fatal(err)
	}
	if jobStatus(t, m, 1) != "partial" {
		t.Fatal("stale read reset job")
	}
	got, err := library.GetBooksData(m.w.libraryDB, []int64{1})
	if err != nil || got[0].Title != "刚刚人工保存" {
		t.Fatal(got, err)
	}
}

func TestLectureRecheckWhitespaceFilename(t *testing.T) {
	m, file := lectureFixture(t)
	moved := filepath.Join(filepath.Dir(file), "030919存在与人生-何光沪 .txt")
	if err := os.Rename(file, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := m.w.libraryDB.Exec(`UPDATE book_formats SET path=? WHERE book_id=1`, moved); err != nil {
		t.Fatal(err)
	}
	if err := m.discoverLectureRechecks(); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckLectureOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lectureCount(t, m, "repaired") != 1 {
		t.Fatal("normalized filename fallback not repaired")
	}
}

package httpapi

import (
	"context"
	"strings"
	"talebook/booksvr/internal/userdb"
	"testing"
)

func TestMetadataEnrollmentCursorPlanAndEmptyBackoffEvents(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	if e := m.saveJob(1, "complete", "", 0); e != nil {
		t.Fatal(e)
	}
	conns := []*userdb.PluginConnection{{ID: 701, OwnerType: "site"}}
	if e := m.enrollSources(conns); e != nil {
		t.Fatal(e)
	}
	if !m.enrollmentNext[701].After(m.now()) {
		t.Fatal("empty tail not backed off")
	}
	if e := m.saveJob(1, "network_pending", "", 0); e != nil {
		t.Fatal(e)
	}
	if e := m.enrollSources(conns); e != nil {
		t.Fatal(e)
	}
	var count int
	w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs WHERE book_id=1 AND connection_id=701`).Scan(&count)
	if count != 1 {
		t.Fatal("backoff lost a status-change event", count)
	}
	conns = append(conns, &userdb.PluginConnection{ID: 702, OwnerType: "site"})
	if e := m.enrollSources(conns); e != nil {
		t.Fatal(e)
	}
	w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs WHERE book_id=1 AND connection_id=702`).Scan(&count)
	if count != 1 {
		t.Fatal("new source failed to backfill", count)
	}
	rows, e := w.libraryDB.Query(`EXPLAIN QUERY PLAN `+metadataEnrollmentSQL, 999999)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
			t.Fatal(e)
		}
		plan += detail
	}
	if !strings.Contains(plan, "metadata_enrollment_pending") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal(plan)
	}
}

func TestMetadataDeletedBookCannotRecreateJobsOrLateResults(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	if e := m.saveJob(1, "file_retry", "pending", 0); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`INSERT INTO metadata_source_jobs(book_id,connection_id) VALUES(1,701)`, `INSERT INTO metadata_source_pending(book_id) VALUES(1)`, `INSERT INTO metadata_lecture_rechecks(book_id,source_name) VALUES(1,'one.txt')`, `INSERT OR IGNORE INTO metadata_field_locks(book_id,field) VALUES(1,'title')`} {
		if _, e := w.libraryDB.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := w.libraryDB.Exec(`DELETE FROM books WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if e := m.saveJob(1, "network_pending", "late", 0); e != nil {
		t.Fatal(e)
	}
	if e := m.local(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	if e := m.finishSource(sourceResult{task: sourceTask{book: 1, connection: 701, phase: "search"}}, w.settings); e != nil {
		t.Fatal("late result", e)
	}
	for _, table := range []string{"metadata_enrichment_jobs", "metadata_source_jobs", "metadata_source_pending"} {
		var count int
		if e := w.libraryDB.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE book_id=1`).Scan(&count); e != nil || count != 0 {
			t.Fatal(table, count, e)
		}
	}
	if e := m.lectureResult(1, "unreadable", "late", 0); e != nil {
		t.Fatal(e)
	}
	var lecture string
	w.libraryDB.QueryRow(`SELECT status FROM metadata_lecture_rechecks WHERE book_id=1`).Scan(&lecture)
	if lecture != "deleted" {
		t.Fatal("lecture history lost or reactivated", lecture)
	}
	var locks int
	w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_field_locks WHERE book_id=1`).Scan(&locks)
	if locks == 0 {
		t.Fatal("manual protection history removed")
	}
}

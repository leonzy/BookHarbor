package scan

import (
	"strings"
	"talebook/booksvr/internal/userdb"
	"testing"
)

func TestStatisticsBooksFilesAndConfirmedMissing(t *testing.T) {
	db, lib := importTestDB(t), importLibraryDB(t)
	svc := newImportService(t, db, lib)
	for _, q := range []string{
		`INSERT INTO books(id,title) VALUES(1,'西游记'),(2,'三国演义')`,
		`CREATE TABLE book_unavailable_sources(book_id INTEGER PRIMARY KEY)`,
		`INSERT INTO book_unavailable_sources VALUES(2),(999)`,
	} {
		if _, err := lib.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	seedStatusRow(t, db, "epub", userdb.ScanStatusIndexed, 1, 1)
	seedStatusRow(t, db, "mobi", userdb.ScanStatusIndexed, 1, 1)
	seedStatusRow(t, db, "copy", userdb.ScanStatusImported, 1, 1)
	seedStatusRow(t, db, "dup", userdb.ScanStatusExist, 1, 1)
	seedStatusRow(t, db, "skip", userdb.ScanStatusDrop, 1, 1)
	seedStatusRow(t, db, "bad", userdb.ScanStatusFailed, 1, 1)
	seedStatusRow(t, db, "new", userdb.ScanStatusReady, 1, 1)
	seedStatusRow(t, db, "container", "expanded", 1, 1)
	got, err := svc.Summary()
	want := Summary{Total: 7, Done: 3, Skipped: 2, Failed: 1, Todo: 1, LibraryBooks: 2, AvailableBooks: 1, MissingSourceBooks: 1}
	if err != nil || got != want {
		t.Fatalf("got %+v err %v want %+v", got, err, want)
	}
	for _, filter := range []ListFilter{FilterDone, FilterTodo, FilterSkipped} {
		rows, _, err := svc.List(ListOptions{Filter: filter, Sort: "id", Num: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if (row.Status == userdb.ScanStatusExist) != (filter == FilterSkipped && row.Hash == "dup") {
				t.Fatalf("duplicate leaked into %s: %+v", filter, row)
			}
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM scanfiles WHERE hash='dup'`).Scan(&status); err != nil || status != userdb.ScanStatusExist {
		t.Fatalf("historical row rewritten: %s %v", status, err)
	}
}

func TestStatisticsLibraryErrorsNotZeroSuccess(t *testing.T) {
	svc := newImportService(t, importTestDB(t), closedDB(t))
	if _, err := svc.Summary(); err == nil {
		t.Fatal("closed library must report error")
	}
}

func TestStatisticsCompletedQueueIncludesUnknownPending(t *testing.T) {
	db := importTestDB(t)
	svc := newImportService(t, db, importLibraryDB(t))
	seedStatusRow(t, db, "indexed", userdb.ScanStatusIndexed, 1, 1)
	seedStatusRow(t, db, "dup", userdb.ScanStatusExist, 1, 1)
	rows, total, err := svc.List(ListOptions{Filter: FilterTodo, Sort: "create_time", Num: 100})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("finished queue: %+v %d %v", rows, total, err)
	}
	seedStatusRow(t, db, "unknown", "future_pending", 2, 2)
	rows, total, err = svc.List(ListOptions{Filter: FilterTodo, Sort: "create_time", Num: 100})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Hash != "unknown" {
		t.Fatalf("lost future pending: %+v %d %v", rows, total, err)
	}
}

func TestImportStatisticsCoveringIndexes(t *testing.T) {
	db := importTestDB(t)
	for i := 0; i < 2; i++ {
		if err := EnsureQueryIndexes(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ q, index string }{
		{`SELECT MAX(import_id) FROM scanfiles`, "scanfiles_import_status_idx"},
		{`SELECT MAX(scan_id) FROM scanfiles`, "scanfiles_scan_status_idx"},
		{`SELECT MAX(create_time) FROM scanfiles`, "scanfiles_created_idx"},
		{`SELECT status,COUNT(*) FROM scanfiles WHERE import_id=1 AND status!='expanded' GROUP BY status`, "scanfiles_import_status_idx"},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + tc.q)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var s string
			if err := rows.Scan(&a, &b, &c, &s); err != nil {
				t.Fatal(err)
			}
			plan += s
		}
		rows.Close()
		if !strings.Contains(plan, "COVERING INDEX "+tc.index) {
			t.Fatalf("not covering: %s", plan)
		}
	}
}

func TestImportListFetchesOnlyCoveredPageKeys(t *testing.T) {
	db := importTestDB(t)
	if err := EnsureQueryIndexes(db); err != nil {
		t.Fatal(err)
	}
	svc := newImportService(t, db, importLibraryDB(t))
	for _, row := range []struct{ hash, status string }{
		{"a", userdb.ScanStatusDrop}, {"b", userdb.ScanStatusExist}, {"c", userdb.ScanStatusIndexed}, {"d", userdb.ScanStatusDrop}, {"e", userdb.ScanStatusExist},
	} {
		seedStatusRow(t, db, row.hash, row.status, 1, 1)
	}
	for page, want := range [][]string{{"e", "d"}, {"b", "a"}} {
		rows, total, err := svc.List(ListOptions{Filter: FilterSkipped, Sort: "create_time", Desc: true, Page: page, Num: 2})
		if err != nil || total != 4 || len(rows) != 2 {
			t.Fatalf("page %d: %d %+v %v", page, total, rows, err)
		}
		for i, row := range rows {
			if row.Hash != want[i] {
				t.Fatalf("page %d item %d = %s", page, i, row.Hash)
			}
		}
	}
	q := pagedListQuery("id,data", ` WHERE status!='expanded' AND status IN (?,?)`, "create_time", "DESC", "scanfiles_status_created_idx")
	rows, err := db.Query("EXPLAIN QUERY PLAN "+q, "drop", "exist", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var a, b, c int
		var s string
		if err := rows.Scan(&a, &b, &c, &s); err != nil {
			t.Fatal(err)
		}
		plan += s + "\n"
	}
	rows.Close()
	if !strings.Contains(plan, "COVERING INDEX scanfiles_status_created_idx") || !strings.Contains(plan, "INTEGER PRIMARY KEY") {
		t.Fatalf("unbounded row lookup: %s", plan)
	}
}

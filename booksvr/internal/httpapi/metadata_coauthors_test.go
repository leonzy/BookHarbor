package httpapi

import (
	"context"
	"os"
	"testing"

	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
)

func TestCoauthorRecheckPreservesActiveSourceAndUnrelatedCursors(t *testing.T) {
	for _, mode := range []string{"readable", "protected", "missing"} {
		t.Run(mode, func(t *testing.T) {
			w, _, path := referenceFixture(t)
			m := testMetadataWorker(t, w)
			if _, err := w.libraryDB.Exec(`CREATE TABLE book_identity_keys(book_id INTEGER PRIMARY KEY,title_key TEXT,author_key TEXT);CREATE INDEX test_identity_title ON book_identity_keys(title_key,author_key);CREATE TABLE book_identity_state(key TEXT PRIMARY KEY,value INTEGER)`); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("作者：甲 & 乙\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := library.FillMissingMetadata(w.libraryDB, 1, importmeta.Metadata{Authors: []string{"甲"}}, "file"); err != nil {
				t.Fatal(err)
			}
			insertBook(t, w.libraryDB, 2, "Selected title (original edition)")
			if _, err := library.FillMissingMetadata(w.libraryDB, 2, importmeta.Metadata{Authors: []string{"甲", "乙"}}, "file"); err != nil {
				t.Fatal(err)
			}
			if _, err := w.libraryDB.Exec(`INSERT INTO book_identity_keys VALUES(1,'same-title','甲'),(2,'same-title','甲'||char(31)||'乙')`); err != nil {
				t.Fatal(err)
			}
			if err := m.setState("history_cursor", 123); err != nil {
				t.Fatal(err)
			}
			w.libraryDB.Exec(`INSERT OR REPLACE INTO book_identity_state(key,value) VALUES('authors_v5_done',1)`)
			if err := m.saveJob(1, "network_pending", "", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,record,next_at) VALUES(1,99,'detail','{"title":"active"}',12345)`); err != nil {
				t.Fatal(err)
			}
			if err := m.discoverCoauthorRechecks(); err != nil {
				t.Fatal(err)
			}
			if mode == "protected" {
				w.libraryDB.Exec(`INSERT INTO metadata_field_locks(book_id,field) VALUES(1,'authors')`)
			}
			if mode == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.recheckCoauthorOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			var status, phase, record string
			var at int64
			if err := w.libraryDB.QueryRow(`SELECT status FROM metadata_coauthor_rechecks WHERE book_id=1`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "done"
			if mode == "protected" {
				want = "protected"
			}
			if mode == "missing" {
				want = "unreadable"
			}
			if status != want {
				t.Fatal(status, want)
			}
			w.libraryDB.QueryRow(`SELECT phase,record,next_at FROM metadata_source_jobs WHERE connection_id=99`).Scan(&phase, &record, &at)
			if phase != "detail" || record != `{"title":"active"}` || at != 12345 || jobStatus(t, m, 1) != "network_pending" {
				t.Fatal("active work changed", phase, record, at, jobStatus(t, m, 1))
			}
			if cursor, err := m.state("history_cursor"); err != nil || cursor != 123 {
				t.Fatal("cursor reset", cursor, err)
			}
			var n int
			w.libraryDB.QueryRow(`SELECT count(*) FROM books_authors_link WHERE book=1`).Scan(&n)
			authors := 1
			if mode == "readable" {
				authors = 2
			}
			if n != authors {
				t.Fatal(n, authors)
			}
		})
	}
}

func TestQueryAndAutomaticMatchShareConservativePromotionCleaning(t *testing.T) {
	title := "从1到N：企业数字化生存指南"
	b := library.BookData{Title: title + "（来自麦肯锡团队的企业数字化指南）", Authors: []string{"尤尔根·梅菲特", "沙莎"}}
	q := referenceQuery(&b)
	if q["title"] != title || b.Title == title {
		t.Fatal(q, b)
	}
	candidate := map[string]any{"title": title, "authors": b.Authors, "provider_value": "one"}
	if matchingMetadata(b, []map[string]any{candidate}) == nil {
		t.Fatal("clean title not used for match")
	}
	b.Authors = []string{"【德】尤尔根·梅菲特（Jürgen Meffert） 沙莎"}
	if len(toStringSlice(referenceQuery(&b)["authors"])) != 2 || len(b.Authors) != 1 {
		t.Fatal("bilingual credits not used for lookup or mutated")
	}
	if matchingMetadata(b, []map[string]any{candidate}) == nil {
		t.Fatal("bilingual credits not used for matching")
	}
	b.Title = title + "（修订版）"
	if matchingMetadata(b, []map[string]any{candidate}) != nil {
		t.Fatal("edition qualifier erased")
	}
}

func TestNetworkCandidateCannotReuseOneAuthorToHideCoauthorConflict(t *testing.T) {
	book := library.BookData{Title: "同名作品", Authors: []string{"甲", "乙"}}
	candidate := map[string]any{"title": book.Title, "authors": []string{"甲", "丙"}, "isbn": "9787313191304", "provider_value": "one"}
	for _, isbn := range []string{"", "9787313191304"} {
		book.ISBN = isbn
		if referenceRelevant(book, candidate) || matchingMetadata(book, []map[string]any{candidate}) != nil {
			t.Fatal("conflicting coauthor accepted", isbn)
		}
	}
	candidate["authors"] = []string{"甲"}
	book.ISBN = ""
	if !referenceRelevant(book, candidate) || matchingMetadata(book, []map[string]any{candidate}) == nil {
		t.Fatal("incomplete source authors rejected")
	}
	book = library.BookData{Title: "Freakonomics", Authors: []string{"Steven D. Levitt"}, ISBN: "9787313191304"}
	candidate = map[string]any{"title": book.Title, "authors": []string{"Steven Levitt"}, "isbn": book.ISBN, "provider_value": "one"}
	if matchingMetadata(book, []map[string]any{candidate}) == nil {
		t.Fatal("existing strong-ISBN alias regressed")
	}
}

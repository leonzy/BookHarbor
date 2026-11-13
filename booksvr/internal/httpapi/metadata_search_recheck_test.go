package httpapi

import (
	"context"
	"os"
	"testing"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginruntime"
)

func TestTitleOnlySearchRunsButCannotAssignNamesake(t *testing.T) {
	w, _, path := referenceFixture(t)
	os.WriteFile(path, []byte("Only ordinary prose."), 0600)
	w.libraryDB.Exec(`DELETE FROM books_authors_link WHERE book=1`)
	w.libraryDB.Exec(`UPDATE books SET author_sort='佚名',title='Selected title' WHERE id=1`)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	if _, err := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); err != nil {
		t.Fatal(err)
	}
	if err := m.local(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if got := jobStatus(t, m, 1); got != "network_pending" {
		t.Fatal(got)
	}
	if err := runMetadataNetwork(t, m); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs WHERE book_id=1`).Scan(&n); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	b, err := bookGetOne(w.Config(""), nil, 1)
	if err != nil || b.Publisher != "" || b.ISBN != "" {
		t.Fatal(b, err)
	}
}

func TestSearchEligibilityDoesNotConfuseNumericTitlesWithMissingTitles(t *testing.T) {
	for _, title := range []string{"1984", "活", "S", "登天的感觉"} {
		for _, authors := range [][]string{{"佚名"}, {"Known author"}} {
			book := library.BookData{Title: title, Authors: authors}
			if !metadataSearchable(book) {
				t.Fatal("valid title blocked", book)
			}
		}
	}
	for _, title := range []string{"", "   ", "——"} {
		if metadataSearchable(library.BookData{Title: title}) {
			t.Fatal("missing search terms", title)
		}
	}
	book := library.BookData{Title: "1984", Authors: []string{"Unknown"}}
	query := referenceQuery(&book)
	if len(toStringSlice(query["authors"])) != 0 || book.Authors[0] != "Unknown" {
		t.Fatal("placeholder included or book mutated", query, book)
	}
}

func TestSearchGateRepairKeepsActiveJobsAndManualLocks(t *testing.T) {
	w, _, path := referenceFixture(t)
	m := testMetadataWorker(t, w)
	os.WriteFile(path, []byte("作者：文件作者\n"), 0600)
	if err := m.saveJob(1, "insufficient", "缺少有效 ISBN，且无法确认作者，等待补充身份信息", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,record,next_at,outcome) VALUES(1,99,'detail','{"title":"active"}',12345,'')`); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckSearchEligibility(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := jobStatus(t, m, 1); got != "network_pending" {
		t.Fatal(got)
	}
	var phase, record string
	var at int64
	w.libraryDB.QueryRow(`SELECT phase,record,next_at FROM metadata_source_jobs WHERE connection_id=99`).Scan(&phase, &record, &at)
	if phase != "detail" || record != `{"title":"active"}` || at != 12345 {
		t.Fatal(phase, record, at)
	}
	if err := w.updateBookMeta(1, "人工标题", map[string]any{"authors": []string{}, "isbn": ""}); err != nil {
		t.Fatal(err)
	}
	if err := RefreshFileMetadata(context.Background(), w.libraryDB, "", 1); err != nil {
		t.Fatal(err)
	}
	b, err := bookGetOne(w.Config(""), nil, 1)
	if err != nil || b.Title != "人工标题" || len(b.Authors) != 0 {
		t.Fatal(b, err)
	}
}

func TestMissingSubtitleEnrichesWorkWithoutGuessingEdition(t *testing.T) {
	b := library.BookData{Title: "登天的感觉", Authors: []string{"岳晓东"}}
	r := map[string]any{"title": "登天的感觉：我在哈佛大学做心理咨询", "authors": []string{"岳晓东"}, "isbn": "9787550266599", "publisher": "北京联合出版公司", "pubdate": "2016", "comments": "作品介绍", "provider_value": "a"}
	got := matchingMetadata(b, []map[string]any{r})
	if got == nil || stringOf(got["comments"]) != "作品介绍" || stringOf(got["isbn"]) != "" || stringOf(got["publisher"]) != "" || stringOf(got["pubdate"]) != "" {
		t.Fatal(got)
	}
	if stringOf(r["isbn"]) == "" {
		t.Fatal("mutated source result")
	}
	b.Authors = []string{"弄简"}
	if matchingMetadata(b, []map[string]any{r}) != nil {
		t.Fatal("namesake assigned")
	}
	b.Authors = []string{"岳晓东"}
	b.Title = "登天的感觉：另一副标题"
	if matchingMetadata(b, []map[string]any{r}) != nil {
		t.Fatal("distinct subtitle assigned")
	}
}

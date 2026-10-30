package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginruntime"
	"testing"
	"time"
)

func testMetadataWorker(t *testing.T, w *bookWiring) *metadataWorker {
	t.Helper()
	if err := library.EnsureMetadataSchema(w.libraryDB); err != nil {
		t.Fatal(err)
	}
	return &metadataWorker{w: w, owner: "fixture-owner", now: func() time.Time { return time.Unix(1800000000, 0) }, logger: log.New(io.Discard, "", 0)}
}

func TestMetadataWorkerFileThenNetworkAndUserLocks(t *testing.T) {
	w, _, path := referenceFixture(t)
	os.WriteFile(path, []byte("作者：Selected author\nISBN：9780141439518\n"), 0600)
	if _, err := w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	worker := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	if _, err := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); err != nil {
		t.Fatal(err)
	}
	if err := worker.local(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	b, err := bookGetOne(w.Config(""), 1)
	if err != nil || b.ISBN != "9780141439518" || b.Authors[0] != "Selected author" {
		t.Fatal(b, err)
	}
	if err := runMetadataNetwork(t, worker); err != nil {
		t.Fatal(err)
	}
	b, err = bookGetOne(w.Config(""), 1)
	if err != nil || b.Publisher != "Selected press" {
		t.Fatal(b, err)
	}
	// A manual clear must remain empty even when a later file/network value exists.
	if err = w.updateBookMeta(1, "Selected title", map[string]any{"isbn": "", "publisher": "", "authors": []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = library.FillMissingMetadata(w.libraryDB, 1, importmeta.Metadata{ISBN: "9780141439518", Publisher: "replacement", Authors: []string{"replacement"}}); err != nil {
		t.Fatal(err)
	}
	b, err = bookGetOne(w.Config(""), 1)
	if err != nil || b.ISBN != "" || b.Publisher != "" || len(b.Authors) != 0 {
		t.Fatal(b, err)
	}
}

func TestMetadataStrongMatchRejectsNamesakesAndAmbiguousEditions(t *testing.T) {
	b := library.BookData{Title: "同名书", Authors: []string{"作者甲"}}
	good := map[string]any{"title": "同名书", "authors": []string{"作者甲"}, "isbn": "9787020091621", "provider_value": "one"}
	wrong := map[string]any{"title": "同名书", "authors": []string{"作者乙"}}
	other := map[string]any{"title": "同名书", "authors": []string{"作者甲"}, "isbn": "9787020121625", "provider_value": "two"}
	if matchingMetadata(b, []map[string]any{wrong}) != nil {
		t.Fatal("namesake accepted")
	}
	if matchingMetadata(b, []map[string]any{good}) == nil {
		t.Fatal("exact author/title rejected")
	}
	if matchingMetadata(b, []map[string]any{good, other}) != nil {
		t.Fatal("ambiguous editions accepted")
	}
	b.ISBN = "9787020091621"
	if matchingMetadata(b, []map[string]any{other}) != nil {
		t.Fatal("conflicting ISBN accepted")
	}
	if isbn13("0306406152") != "9780306406157" {
		t.Fatal("ISBN-10 equivalence")
	}
}

func TestMetadataLeaseCursorsNewPriorityAndRestart(t *testing.T) {
	w, _, _ := referenceFixture(t)
	first := testMetadataWorker(t, w)
	ok, err := first.lease()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	second := testMetadataWorker(t, w)
	second.owner = "second"
	ok, err = second.lease()
	if err != nil || ok {
		t.Fatal("duplicate worker", ok, err)
	}
	id, key, err := first.nextBook()
	if err != nil || id != 1 || key != "history_cursor" {
		t.Fatal(id, key, err)
	}
	insertBook(t, w.libraryDB, 2, "new arrival")
	id, key, err = first.nextBook()
	if err != nil || id != 2 || key != "new_cursor" {
		t.Fatal(id, key, err)
	}
	if err = first.setState(key, id); err != nil {
		t.Fatal(err)
	}
	id, key, err = second.nextBook()
	if err != nil || id != 1 || key != "history_cursor" {
		t.Fatal("resume", id, key, err)
	}
	if err = first.setState(key, id); err != nil {
		t.Fatal(err)
	}
	id, _, err = first.nextBook()
	if err != nil || id != 0 {
		t.Fatal(id, err)
	}
	second.now = func() time.Time { return first.now().Add(121 * time.Second) }
	ok, err = second.lease()
	if err != nil || !ok {
		t.Fatal("expired lease", ok, err)
	}
}

func TestMetadataMissingSourceDoesNotChangeImportOrSourceState(t *testing.T) {
	w, _, path := referenceFixture(t)
	worker := testMetadataWorker(t, w)
	os.Remove(path)
	if err := worker.local(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := w.libraryDB.QueryRow(`SELECT status,last_error FROM metadata_enrichment_jobs WHERE book_id=1`).Scan(&status, &reason); err != nil || status != "file_retry" || reason == "" {
		t.Fatal(status, reason, err)
	}
	b, err := bookGetOne(w.Config(""), 1)
	if err != nil || b == nil || b.Title != "Selected title (original edition)" {
		t.Fatal(b, err)
	}
	var count int
	if err = w.libraryDB.QueryRow(`SELECT count(*) FROM book_formats WHERE book_id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatal("source removed", count, err)
	}
}

func TestMetadataNetworkToggleAndFailureBackoff(t *testing.T) {
	w, provider, path := referenceFixture(t)
	worker := testMetadataWorker(t, w)
	os.WriteFile(path, []byte("作者：Selected author\nISBN：9780141439518\n"), 0600)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	if _, err := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); err != nil {
		t.Fatal(err)
	}
	if err := worker.local(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := runMetadataNetwork(t, worker); err != nil {
		t.Fatal(err)
	}
	var status string
	w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=1`).Scan(&status)
	if status != "network_pending" {
		t.Fatal("disabled switch ignored", status)
	}
	w.settings["auto_fill_meta"] = true
	provider.fail = true
	if err := runMetadataNetwork(t, worker); err != nil {
		t.Fatal(err)
	}
	var next int64
	var attempts int
	if err := w.libraryDB.QueryRow(`SELECT next_at,attempts,last_error FROM metadata_source_jobs WHERE book_id=1`).Scan(&next, &attempts, &status); err != nil || next <= worker.now().Unix() || attempts != 1 || status == "" {
		t.Fatal(next, attempts, status, err)
	}
}

func TestMetadataOnlyRoutesAndGuestProtection(t *testing.T) {
	w, _, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	for _, addr := range []string{"0.0.0.0:8004", "192.168.1.1:8004"} {
		if _, err := newMetadataOnlyDispatcher(ServeOptions{Addr: addr}, w.libraryDB, w.db); err == nil {
			t.Fatal("public maintenance listener")
		}
	}
	d, err := newMetadataOnlyDispatcher(ServeOptions{Addr: "127.0.0.1:8004", CookieSecret: "fixture", Settings: w.settings, PluginRegistry: w.registry}, w.libraryDB, w.db)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/metadata/enrichment", nil))
	if rec.Code == http.StatusOK && rec.Body.String() == "" {
		t.Fatal("empty guest response")
	}
	if !strings.Contains(rec.Body.String(), "user.need_login") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestMetadataCoverOptionPreservesExistingArtwork(t *testing.T) {
	for _, mode := range []string{"metadata-only", "fill-cover", "existing-cover"} {
		t.Run(mode, func(t *testing.T) {
			w, p, path := referenceFixture(t)
			worker := testMetadataWorker(t, w)
			w.settings["auto_fill_meta"] = true
			w.settings["auto_fill_keep_cover"] = mode == "metadata-only"
			img := image.NewRGBA(image.Rect(0, 0, 3, 3))
			img.Set(0, 0, color.RGBA{R: 255, A: 255})
			var buf bytes.Buffer
			png.Encode(&buf, img)
			p.cover = buf.Bytes()
			if mode == "existing-cover" {
				if _, err := library.StoreCover(w.libraryDB, w.libraryRoot, 1, buf.Bytes()); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(path, []byte("作者：Selected author\nISBN：9780141439518\n"), 0600)
			w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
			if _, err := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); err != nil {
				t.Fatal(err)
			}
			cover := library.ManagedCoverPath(w.libraryRoot, 1)
			before, _ := os.ReadFile(cover)
			if err := worker.local(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			if err := runMetadataNetwork(t, worker); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(cover)
			if mode == "metadata-only" {
				if err == nil {
					t.Fatal("cover fetched while disabled")
				}
			} else if err != nil || len(after) == 0 {
				t.Fatal(err)
			}
			if mode == "existing-cover" && !bytes.Equal(before, after) {
				t.Fatal("existing cover replaced")
			}
			if mode == "fill-cover" {
				if files, _ := filepath.Glob(filepath.Join(filepath.Dir(cover), ".cover-*")); len(files) > 0 {
					t.Fatal("temporary cover retained")
				}
			}
		})
	}
}

func TestExplicitReferenceRefreshesLocalSynopsisAndKeepsLocks(t *testing.T) {
	w, _, path := referenceFixture(t)
	worker := testMetadataWorker(t, w)
	if err := worker.saveJob(1, "network_pending", "previously checked", 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("【内容简介】\n新提取的简介。\n\n相关设定\n忽略"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := worker.local(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := w.libraryDB.QueryRow(`SELECT text FROM comments WHERE book=1`).Scan(&got); err != nil || got != "新提取的简介。" {
		t.Fatal(got, err)
	}
	if err := w.updateBookMeta(1, "Selected title", map[string]any{"comments": ""}); err != nil {
		t.Fatal(err)
	}
	if err := worker.local(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	var count int
	w.libraryDB.QueryRow(`SELECT count(*) FROM comments WHERE book=1 AND trim(text)<>''`).Scan(&count)
	if count != 0 {
		t.Fatal("manual clear overwritten")
	}
}

func TestFilenameTitleUsedForLookupWithoutChangingDisplay(t *testing.T) {
	book := library.BookData{Title: "《星海之旅》（校对版全本）作者：作者甲", Authors: []string{"作者甲"}}
	if referenceQuery(&book)["title"] != "星海之旅" || !referenceRelevant(book, map[string]any{"title": "星海之旅", "authors": []string{"作者甲"}}) {
		t.Fatal(book)
	}
	if matchingMetadata(book, []map[string]any{{"title": "星海之旅", "authors": []string{"作者甲"}}}) == nil {
		t.Fatal("exact filename title/author rejected")
	}
	if book.Title != "《星海之旅》（校对版全本）作者：作者甲" {
		t.Fatal("display title overwritten")
	}
}

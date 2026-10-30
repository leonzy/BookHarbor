package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcefile"
	"testing"
	"time"
)

func TestMetadataRejectsEchoedISBNAndUnrelatedTitles(t *testing.T) {
	known := library.BookData{Title: "Known title", Authors: []string{"Known author"}, ISBN: "9787020091621"}
	if matchingMetadata(known, []map[string]any{{"title": "Known title", "authors": []string{"Known author"}}}) != nil {
		t.Fatal("known ISBN fell back to an unverified edition")
	}

	book := library.BookData{Title: "（老舍作品名家插图系列）微神集·月牙集（袁运生插图本）", Authors: []string{"老舍"}, ISBN: "9787020091621"}
	for _, tt := range []struct {
		title, author, isbn string
		want                bool
	}{
		{"赤朽叶家的传说", "樱庭一树", "9787020091621", false},
		{"微神集·月牙集 : 老舍作品名家插图系列", "老舍", "9787020091621", true},
		{"微神集月牙集(袁运生插图本)(精)/老舍作品名家插图系列", "老舍", "", true},
		{"微神集·月牙集", "老舍 著", "", true},
		{"微神集·月牙集", "其他作者", "", false},
		{"微神集·月牙集", "老舍", "9780141439518", false},
	} {
		t.Run(tt.title+tt.author+tt.isbn, func(t *testing.T) {
			r := map[string]any{"title": tt.title, "authors": []string{tt.author}, "isbn": tt.isbn}
			if got := referenceRelevant(book, r); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			if !tt.want && matchingMetadata(book, []map[string]any{r}) != nil {
				t.Fatal("unrelated candidate selected automatically")
			}
		})
	}
}
func TestMetadataProgressCountsSparseIDsAndDeletedJobs(t *testing.T) {
	w, _, _ := referenceFixture(t)
	worker := testMetadataWorker(t, w)
	insertBook(t, w.libraryDB, 9000, "Sparse ID")
	worker.saveJob(1, "network_pending", "retry", time.Hour)
	worker.saveJob(9999, "complete", "deleted book", 0)
	w.libraryDB.Exec(`UPDATE metadata_enrichment_jobs SET attempts=1 WHERE book_id=1`)
	result, e := MetadataEnrichmentStatus(w.libraryDB, ServeOptions{Settings: map[string]any{"auto_fill_meta": true}})(adminCtx(1))
	if e != nil {
		t.Fatal(e)
	}
	for key, want := range map[string]int64{"total_books": 2, "files_checked": 1, "files_remaining": 1, "network_retries": 1, "network_applied_books": 0} {
		if result[key] != want {
			t.Fatal(key, result[key], want)
		}
	}
	if result["network_enabled"] != true {
		t.Fatal(result)
	}
}
func TestStoragePathsPerFormatWithoutReadingSources(t *testing.T) {
	w, _, _ := referenceFixture(t)
	plain := filepath.Join(t.TempDir(), "offline", "source.epub")
	ref := sourcefile.Encode(sourcefile.Reference{Archive: filepath.Join(t.TempDir(), "books.zip"), Name: "book.mobi", Chain: []string{"目录/book.mobi"}, Hash: "sha256:" + strings.Repeat("a", 64), Size: 100, Fingerprint: "fingerprint"})
	for format, path := range map[string]string{"EPUB": plain, "MOBI": ref} {
		if _, e := w.libraryDB.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,?,?,100,0)`, format, path); e != nil {
			t.Fatal(e)
		}
	}
	book := library.BookData{ID: 1, Formats: []string{"EPUB", "MOBI"}}
	admin := adminCtx(1)
	cfg := w.Config("")
	files := bookFileEntries(cfg, admin, book)
	if len(files) != 2 {
		t.Fatal(files)
	}
	for _, file := range files {
		location := file["storage"].(map[string]any)
		if location["directory"] == "" {
			t.Fatal(file)
		}
		if file["format"] == "MOBI" && location["archive_member"] != "目录/book.mobi" {
			t.Fatal(file)
		}
	}
	guest := pluginCtx(t, w.db, http.MethodGet, "/api/book/1", "", 0, false)
	for _, file := range bookFileEntries(cfg, guest, book) {
		if file["storage"] != nil {
			t.Fatal("storage path leaked to guest")
		}
	}
}
func TestImageRedirectRevalidatesHostAndContent(t *testing.T) {
	for _, tt := range []struct {
		name, location, content string
		want, calls             int
	}{
		{"allowed redirect", "https://cdn.example/cover", "image/png", 200, 2},
		{"private redirect", "http://127.0.0.1/secrets", "image/png", 403, 1},
		{"html is not image", "", "text/html", 502, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cfg := filesFixture(t)
			calls := 0
			cfg.AllowsImageProxyHost = func(h string) bool { return h == "ok.example" || h == "cdn.example" }
			cfg.FetchImage = func(_ context.Context, url string) (int, http.Header, []byte, error) {
				calls++
				if calls == 1 && tt.location != "" {
					return 302, http.Header{"Location": []string{tt.location}}, nil, nil
				}
				return 200, http.Header{"Content-Type": []string{tt.content}}, []byte("picture"), nil
			}
			ctx.Request = httptest.NewRequest(http.MethodGet, "/get/pcover?url=https://ok.example/cover", nil)
			ProxyImageHandler(cfg)(ctx)
			rec := ctx.Writer.(*httptest.ResponseRecorder)
			if rec.Code != tt.want || calls != tt.calls {
				t.Fatal(fmt.Sprint(rec.Code, calls), tt.want, tt.calls)
			}
		})
	}
}

func TestDefaultCoverFetchBlocksPrivateAddresses(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png"))
	}))
	defer server.Close()
	_, _, _, e := DefaultFetchImage(context.Background(), server.URL+"/cover")
	if e == nil || calls != 0 {
		t.Fatal("private endpoint was fetched", e, calls)
	}
}

func TestUnrelatedCandidateIsFilteredAndCannotBeApplied(t *testing.T) {
	w, p, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	p.unrelated = true
	w.libraryDB.Exec(`INSERT INTO identifiers(book,type,val) VALUES(1,'isbn','9780141439518')`)
	ctx := adminCtx(1)
	ctx.Params = []string{"1"}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/book/1/refer", nil)
	result, e := BookReferHandler(w.Config(""))(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(result["books"].([]any)) != 0 || result["summary"].(map[string]any)["filtered"] != 1 {
		t.Fatal(result)
	}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/book/1/refer", strings.NewReader(`{"provider_key":"talebook.test.reference","provider_value":"record-1","only_meta":"yes"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	result, e = BookReferHandler(w.Config(""))(ctx)
	if e != nil || result["err"] != "metadata.mismatch" {
		t.Fatal(result, e)
	}
	b, _ := bookGetOne(w.Config(""), 1)
	if b.Title != "Selected title (original edition)" || b.Publisher != "" {
		t.Fatal("mismatched result changed the book", b)
	}
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginruntime"
)

type classificationProvider struct {
	*referenceProvider
	records []plugindomain.BookMetadata
}

func (p *classificationProvider) Invoke(method string, args []any, ctx map[string]any) (any, error) {
	return pluginbuiltin.DispatchInvoke(p, method, args, ctx)
}
func (p *classificationProvider) SearchBooks(plugindomain.MetadataQuery, map[string]any) ([]plugindomain.BookMetadata, error) {
	if p.fail {
		return nil, fmt.Errorf("upstream failed")
	}
	return p.records, nil
}

func classificationFixture(t *testing.T) (*metadataWorker, *classificationProvider) {
	t.Helper()
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.registry = pluginruntime.NewPluginRegistry()
	p := &classificationProvider{referenceProvider: &referenceProvider{key: "test.classification"}}
	if _, err := w.registry.Register(p, pluginiface.CapabilitySet{Interfaces: map[string]bool{"MetadataProvider": true}, HasSelfCheck: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); err != nil {
		t.Fatal(err)
	}
	w.db.Exec(`UPDATE plugin_installations SET enabled=1`)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	if _, err := library.FillMissingMetadata(w.libraryDB, 1, importmeta.Metadata{Authors: []string{"Selected author"}}, "file:fixture"); err != nil {
		t.Fatal(err)
	}
	w.settings["auto_fill_meta"] = true
	if err := m.saveJob(1, "network_pending", "", 0); err != nil {
		t.Fatal(err)
	}
	return m, p
}
func jobStatus(t *testing.T, m *metadataWorker, id int64) string {
	t.Helper()
	var s string
	if err := m.w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMetadataOutcomesRequireActualCandidates(t *testing.T) {
	for _, kind := range []string{"empty", "unrelated", "ambiguous", "duplicate", "matched"} {
		t.Run(kind, func(t *testing.T) {
			m, p := classificationFixture(t)
			first := map[string]any{"title": "Selected title", "authors": []string{"Selected author"}, "publisher": "First press", "isbn": "9780141439518", "provider_value": "record-1"}
			switch kind {
			case "unrelated":
				p.records = []plugindomain.BookMetadata{plugindomain.NewBookMetadata(map[string]any{"title": "Other book", "authors": []string{"Other author"}, "provider_value": "other"})}
			case "ambiguous":
				p.records = []plugindomain.BookMetadata{plugindomain.NewBookMetadata(first), plugindomain.NewBookMetadata(map[string]any{"title": "Selected title", "authors": []string{"Selected author"}, "publisher": "Second press", "isbn": "9787020091621", "provider_value": "record-2"})}
			case "duplicate":
				p.records = []plugindomain.BookMetadata{plugindomain.NewBookMetadata(first), plugindomain.NewBookMetadata(first)}
			case "matched":
				p.records = []plugindomain.BookMetadata{plugindomain.NewBookMetadata(first)}
			}
			if err := runMetadataNetwork(t, m); err != nil {
				t.Fatal(err)
			}
			want := "complete"
			if kind == "empty" || kind == "unrelated" {
				want = "unmatched"
			}
			if kind == "ambiguous" {
				want = "needs_review"
			}
			if got := jobStatus(t, m, 1); got != want {
				t.Fatal(kind, got, want)
			}
			ctx := adminCtx(1)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			out, err := metadataReviewRoutes(m.w.libraryDB)[0].Handler(ctx)
			if err != nil {
				t.Fatal(err)
			}
			books := out["books"].([]map[string]any)
			if kind == "ambiguous" {
				if len(books) != 1 || len(books[0]["candidates"].([]map[string]any)) != 2 {
					t.Fatal(out)
				}
			} else if len(books) != 0 {
				t.Fatal("noncandidate in review", out)
			}
		})
	}
}

func TestMetadataFailureRetriesOnlyFailedSources(t *testing.T) {
	m, p := classificationFixture(t)
	p.fail = true
	for n := 0; n < 3; n++ {
		next := m.now().Add(10 * time.Minute)
		m.now = func() time.Time { return next }
		if err := runMetadataNetwork(t, m); err != nil {
			t.Fatal(err)
		}
	}
	if got := jobStatus(t, m, 1); got != "source_failed" {
		t.Fatal(got)
	}
	var conn, deadline int64
	m.w.libraryDB.QueryRow(`SELECT connection_id FROM metadata_source_jobs WHERE book_id=1`).Scan(&conn)
	m.w.libraryDB.QueryRow(`SELECT next_at FROM metadata_source_limits WHERE connection_id=?`, conn).Scan(&deadline)
	if _, err := m.w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,outcome,matched) VALUES(1,999,'done','no_match',0)`); err != nil {
		t.Fatal(err)
	}
	next := m.now().Add(2 * time.Hour)
	m.now = func() time.Time { return next }
	if err := m.retryFailedSourceBook(); err != nil {
		t.Fatal(err)
	}
	var phase string
	m.w.libraryDB.QueryRow(`SELECT phase FROM metadata_source_jobs WHERE book_id=1 AND connection_id=999`).Scan(&phase)
	if phase != "done" {
		t.Fatal("healthy source reset")
	}
	var after int64
	m.w.libraryDB.QueryRow(`SELECT next_at FROM metadata_source_limits WHERE connection_id=?`, conn).Scan(&after)
	if after != deadline {
		t.Fatal("source cooldown changed")
	}
	p.fail = false
	p.records = []plugindomain.BookMetadata{p.record()}
	if err := runMetadataNetwork(t, m); err != nil {
		t.Fatal(err)
	}
	if got := jobStatus(t, m, 1); got != "complete" {
		t.Fatal("retry did not recover", got)
	}
}

func TestMetadataHistoricalClassificationIsBoundedAndProtectsWork(t *testing.T) {
	m, _ := classificationFixture(t)
	for id := int64(1); id <= 55; id++ {
		if id > 1 {
			insertBook(t, m.w.libraryDB, id, fmt.Sprint("Book ", id))
			insertItem(t, m.w.db, id, "public", 1)
		}
		if err := m.saveJob(id, "needs_review", "旧分类", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := m.w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,last_error) VALUES(?,1,'done','未找到可唯一确认的版本')`, id); err != nil {
			t.Fatal(err)
		}
	}
	// The first book has authors and finished searches. Others have no identity.
	m.w.libraryDB.Exec(`UPDATE metadata_source_jobs SET last_error='来源请求失败' WHERE book_id=2`)
	library.FillMissingMetadata(m.w.libraryDB, 2, importmeta.Metadata{Authors: []string{"Known author"}}, "file:fixture")
	if err := m.saveJob(55, "protected", "人工保护", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.saveJob(54, "network_pending", "正在处理", 0); err != nil {
		t.Fatal(err)
	}
	m.w.libraryDB.Exec(`UPDATE metadata_source_jobs SET phase='detail',next_at=12345,record='{"title":"active"}' WHERE book_id=54`)
	if err := m.reclassifyReviewBatch(); err != nil {
		t.Fatal(err)
	}
	var remaining int
	m.w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_enrichment_jobs WHERE status='needs_review'`).Scan(&remaining)
	if remaining != 3 {
		t.Fatal("batch was not bounded", remaining)
	}
	if jobStatus(t, m, 1) != "unmatched" || jobStatus(t, m, 2) != "source_failed" || jobStatus(t, m, 3) != "insufficient" {
		t.Fatal("wrong classifications")
	}
	for i := 0; i < 3; i++ {
		if err := m.reclassifyReviewBatch(); err != nil {
			t.Fatal(err)
		}
	}
	if jobStatus(t, m, 54) != "network_pending" || jobStatus(t, m, 55) != "protected" {
		t.Fatal("active or protected job changed")
	}
	var phase, record string
	var at int64
	m.w.libraryDB.QueryRow(`SELECT phase,record,next_at FROM metadata_source_jobs WHERE book_id=54`).Scan(&phase, &record, &at)
	if phase != "detail" || at != 12345 || record != `{"title":"active"}` {
		t.Fatal("active source reset")
	}
	// A completed migration is restart-safe and leaves all source files untouched.
	if err := m.reclassifyReviewBatch(); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataLookupAuthorLabelIsNotMissingIdentity(t *testing.T) {
	b := metadataLookupBook(library.BookData{Title: "《酒神（阴阳冕）》（校对版全本）作者：唐家三少", Authors: []string{"Unknown"}})
	if len(b.Authors) != 1 || b.Authors[0] != "唐家三少" {
		t.Fatal(b)
	}
	b = metadataLookupBook(library.BookData{Title: b.Title, Authors: []string{"人工作者"}})
	if b.Authors[0] != "人工作者" {
		t.Fatal("manual author replaced")
	}
}

func TestMetadataReviewDoesNotExposeLegacyEmptyRecords(t *testing.T) {
	m, _ := classificationFixture(t)
	m.saveJob(1, "needs_review", "旧的空结果", 0)
	ctx := adminCtx(1)
	ctx.Request = httptest.NewRequest("GET", "/", nil)
	out, err := metadataReviewRoutes(m.w.libraryDB)[0].Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out["books"].([]map[string]any)) != 0 {
		t.Fatal(out)
	}
	progress, err := MetadataEnrichmentStatus(m.w.libraryDB, ServeOptions{})(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := progress["counts"].(map[string]int64)
	if counts["needs_review"] != 0 || counts["classification_pending"] != 1 {
		t.Fatal(counts)
	}
	// Ensure a stored candidate cache is bounded JSON rather than encoded image data.
	candidates := metadataReviewCandidates(library.BookData{Title: "T", Authors: []string{"A"}}, []map[string]any{{"title": "T", "authors": []string{"A"}, "provider_value": "1"}, {"title": "T", "authors": []string{"A"}, "provider_value": "2"}})
	if data, err := json.Marshal(candidates); err != nil || len(data) > 32768 {
		t.Fatal(err)
	}
}

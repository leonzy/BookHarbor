package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetadataReviewCursorAndManualSave(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	for i := 1; i <= 23; i++ {
		if i > 1 {
			insertBook(t, w.libraryDB, int64(i), fmt.Sprint("Book ", i))
		}
		if err := m.saveJob(int64(i), "needs_review", "缺少可验证作者", 0); err != nil {
			t.Fatal(err)
		}
	}
	route := metadataReviewRoutes(w.libraryDB)[0]
	if !route.Auth || !route.Admin {
		t.Fatal("review is administrator-only")
	}
	ctx := adminCtx(1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/?after=0", nil)
	out, err := route.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	books := out["books"].([]map[string]any)
	if len(books) != 20 || out["next_after"] != int64(20) || books[0]["reason"] != "缺少可验证作者" {
		t.Fatal(out)
	}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/?after=20", nil)
	out, err = route.Handler(ctx)
	if err != nil || len(out["books"].([]map[string]any)) != 3 || out["next_after"] != int64(0) {
		t.Fatal(out, err)
	}
	for _, raw := range []string{"-1", "bad", "9223372036854775808"} {
		ctx.Request = httptest.NewRequest(http.MethodGet, "/?after="+raw, nil)
		out, err = route.Handler(ctx)
		if err != nil || out["err"] != "params.invalid" {
			t.Fatal(out, err)
		}
	}
	if err = w.updateBookMeta(1, "Manually confirmed", map[string]any{"authors": []string{"Verified author"}}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=1`).Scan(&status); err != nil || status != "protected" {
		t.Fatal(status, err)
	}
	if err = m.saveJob(1, "needs_review", "late asynchronous result", 0); err != nil {
		t.Fatal(err)
	}
	if err = m.rollupSourceBook(1, w.settings); err != nil {
		t.Fatal(err)
	}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/?after=0", nil)
	out, err = route.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out["books"].([]map[string]any)[0]["id"] != int64(2) {
		t.Fatal("saved manual review remains queued", out)
	}
}

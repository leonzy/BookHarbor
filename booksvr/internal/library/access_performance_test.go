package library

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"talebook/booksvr/internal/access"
)

// Opt-in synthetic performance receipt; never opens a configured/site DB.
func TestAccessLargeCatalogueIndexedPages(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("BOOKHARBOR_ACCESS_PERF_ROWS"))
	if n < 1000 {
		t.Skip("set BOOKHARBOR_ACCESS_PERF_ROWS for isolated scale test")
	}
	if n > 1000000 {
		t.Fatal("fixture budget exceeded")
	}
	db := newQueryDB(t)
	if _, err := db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 4 UNION ALL SELECT x+1 FROM seq WHERE x<?) INSERT INTO books(id,title,path,timestamp) SELECT x,'Book '||x,'book/'||x,x FROM seq`, n); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE books SET work_id=(id+2)/3; INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature,managed_work) SELECT id,'','book','author','fixture-'||id,work_id FROM books`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := AccessStore{DB: db}
	actor, err := s.RegisterPrincipal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := AccessBuildToken{Revision: r.ID, Version: r.Version}
	profiles, err := s.BuildAccessProfiles(ctx, token, []string{actor})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for first := 1; first <= n; first += 64 {
		batch := []AccessBookSnapshot{}
		for id := first; id < first+64 && id <= n; id++ {
			batch = append(batch, AccessBookSnapshot{ID: int64(id), Title: fmt.Sprintf("Book %08d", id), Timestamp: fmt.Sprint(id), Files: []AccessFileSnapshot{{ID: fmt.Sprint(id), Version: 1, Format: "EPUB", Size: int64(id), Facts: access.FileFacts{Root: "root", Directory: "/books", Ready: true}}}})
		}
		if err = s.WriteAccessBatch(ctx, token, profiles, batch); err != nil {
			t.Fatal(err)
		}
	}
	build := time.Since(started)
	accessTestCheckpoint(t, s, token)
	if err = s.FinishPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	scope, err := s.PreviewAccess(ctx, r.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	timings := []time.Duration{}
	for i := 0; i < 25; i++ {
		start := time.Now()
		ids, total, e := AccessBrowseBooks(ctx, db, scope, AccessBrowseQuery{BrowseQuery: BrowseQuery{GroupWorks: true, Size: 60, Start: (i % 5) * 60}})
		if e != nil || total != (n+2)/3 || len(ids) != 60 {
			t.Fatal(ids, total, e)
		}
		timings = append(timings, time.Since(start))
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("rows=%d profiles=%d build=%s grouped-page p50=%s p95=%s max=%s", n, len(profiles), build, timings[12], timings[23], timings[24])
	if timings[23] > time.Second {
		t.Fatal("indexed ordinary page exceeds one-second isolated budget")
	}
}

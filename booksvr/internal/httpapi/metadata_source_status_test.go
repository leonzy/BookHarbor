package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"talebook/booksvr/internal/pluginproto"
	"testing"
	"time"

	"talebook/booksvr/internal/pluginruntime"
)

func TestMetadataSourceIntervalValidationAndBackoffProtection(t *testing.T) {
	w, _, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	if _, e := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); e != nil {
		t.Fatal(e)
	}
	sources, e := metadataSourceStatus(w.libraryDB, w.db, ServeOptions{Settings: w.settings, PluginRegistry: w.registry}, adminCtx(1))
	if e != nil || len(sources) != 1 || sources[0]["name"] != "Fixture metadata" {
		t.Fatal("source must be identified by provider, not generic connection name", sources, e)
	}
	var id int64
	w.db.QueryRow(`SELECT id FROM plugin_connections LIMIT 1`).Scan(&id)
	routes := metadataSourceRoutes(w.libraryDB, w.db, ServeOptions{Settings: w.settings, PluginRegistry: w.registry})
	if !routes[0].Auth || !routes[0].Admin {
		t.Fatal("frequency settings must require admin")
	}
	for _, body := range []string{`{}`, `{"interval_seconds":0}`, `{"interval_seconds":3601}`, `{"interval_seconds":1.5}`, `{"interval_seconds":5,"unexpected":1}`} {
		ctx := adminCtx(1)
		ctx.Params = []string{stringOf(id)}
		ctx.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		result, e := routes[0].Handler(ctx)
		if e != nil || result["err"] != "params.invalid" {
			t.Fatal(body, result, e)
		}
	}
	now := time.Now().Unix()
	w.libraryDB.Exec(`INSERT INTO metadata_source_limits(connection_id,next_at,last_request_at,failures) VALUES(?,?,?,1)`, id, now+600, now)
	ctx := adminCtx(1)
	ctx.Params = []string{stringOf(id)}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"interval_seconds":12}`))
	result, e := routes[0].Handler(ctx)
	if e != nil || result["err"] != "ok" {
		t.Fatal(result, e)
	}
	var interval, next int64
	w.libraryDB.QueryRow(`SELECT interval_seconds,next_at FROM metadata_source_limits WHERE connection_id=?`, id).Scan(&interval, &next)
	if interval != 12 || next != now+600 {
		t.Fatal("editing interval bypassed source failure backoff", interval, next)
	}
	ctx.Params = []string{"99999"}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"interval_seconds":12}`))
	result, e = routes[0].Handler(ctx)
	if e != nil || result["err"] != "permission" {
		t.Fatal(result, e)
	}
}

func TestMetadataProgressSeparatesFileRefreshFromUnreadable(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	if e := m.saveJob(1, "file_retry", "TXT 简介规则更新，等待限速文件复核", 0); e != nil {
		t.Fatal(e)
	}
	result, e := MetadataEnrichmentStatus(w.libraryDB, ServeOptions{})(adminCtx(1))
	if e != nil {
		t.Fatal(e)
	}
	counts := result["counts"].(map[string]int64)
	if counts["file_refresh"] != 1 || counts["file_retry"] != 1 {
		t.Fatal(result)
	}
	if result["network_max_concurrency"] != metadataSourceConcurrency || result["source_default_interval_seconds"] != 5 {
		t.Fatal(result)
	}
}

func TestSourceDisabledWhileRunningDoesNotApplyResponse(t *testing.T) {
	w, _, file := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	gate := make(chan struct{})
	started := make(chan string, 1)
	p := &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: "test.toggle"}, started: started, gate: gate}
	registerConcurrentMetadata(t, w, p)
	pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
	w.db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE plugin_key='talebook.test.reference'`)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	// Supply enough identity for a safe match without running file extraction.
	if e := w.updateBookMeta(1, "Selected title", map[string]any{"authors": []string{"Selected author"}}); e != nil {
		t.Fatal(e)
	}
	_ = file
	if e := m.saveJob(1, "network_pending", "", 0); e != nil {
		t.Fatal(e)
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(gate)
		t.Fatal("source did not start")
	}
	w.settings["auto_fill_meta"] = false
	close(gate)
	deadline := time.After(3 * time.Second)
	for len(m.results) == 0 {
		select {
		case <-deadline:
			t.Fatal("source did not return")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	b, _ := bookGetOne(w.Config(""), 1)
	if b.Publisher != "" {
		t.Fatal("disabled worker applied a result", b)
	}
}

func TestSourceQueueDueQueryUsesIndex(t *testing.T) {
	w, _, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	rows, e := w.libraryDB.Query(`EXPLAIN QUERY PLAN SELECT s.book_id FROM metadata_source_jobs s JOIN metadata_enrichment_jobs j ON j.book_id=s.book_id WHERE s.connection_id=? AND s.priority=? AND s.phase<>'done' AND s.next_at<=? AND j.status IN ('network_pending','partial') ORDER BY s.next_at,s.book_id LIMIT 1`, 1, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		if e = rows.Scan(&a, &b, &c, &detail); e != nil {
			t.Fatal(e)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "metadata_source_due") || strings.Contains(joined, "TEMP B-TREE") {
		t.Fatal(joined)
	}
}

func TestSourceFailureBackoffRespectsQuotaAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		code  string
		err   error
		delay int64
	}{
		{"provider_rate_limited", pluginproto.NewUpstreamRateLimitError("secret upstream URL", 1800.0), 1800},
		{"provider_unauthorized", pluginproto.NewUpstreamAuthError("private token", "auth", nil), 3600},
		{"request_failed", fmt.Errorf("sensitive transport details"), 30},
		{"request_failed", fmt.Errorf("transport failure with long interval"), 3600},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w, _, _ := referenceFixture(t)
			m := testMetadataWorker(t, w)
			w.settings["auto_fill_meta"] = true
			pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
			var cid int64
			w.db.QueryRow(`SELECT id FROM plugin_connections LIMIT 1`).Scan(&cid)
			m.saveJob(1, "network_pending", "", 0)
			w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id) VALUES(1,?)`, cid)
			interval := int64(5)
			if tc.code == "request_failed" && tc.delay == 3600 {
				interval = 3600
			}
			w.libraryDB.Exec(`INSERT INTO metadata_source_limits(connection_id,interval_seconds) VALUES(?,?)`, cid, interval)
			if e := m.finishSource(sourceResult{task: sourceTask{book: 1, connection: cid, phase: "search"}, err: tc.err}, w.settings); e != nil {
				t.Fatal(e)
			}
			var next int64
			var code string
			w.libraryDB.QueryRow(`SELECT next_at,last_error FROM metadata_source_limits WHERE connection_id=?`, cid).Scan(&next, &code)
			if next < m.now().Unix()+tc.delay || code != tc.code {
				t.Fatal(next, code)
			}
		})
	}
}
func TestSourcePayloadBoundDropsOpaqueArtworkAndRejectsOversize(t *testing.T) {
	record, ok := boundedSourceRecord(map[string]any{"title": "A book", "_calibre_mi": strings.Repeat("x", 300000)})
	if !ok || len(record) != 1 {
		t.Fatal("opaque payload retained")
	}
	if _, ok := boundedSourceRecord(map[string]any{"title": "A book", "comments": strings.Repeat("x", 300000)}); ok {
		t.Fatal("unbounded metadata accepted")
	}
}

func TestSourceEnrollmentDoesNotQueueBooksForAnotherUsersCredentials(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
	w.db.Exec(`UPDATE plugin_connections SET owner_type='user',owner_id=2`)
	m.saveJob(1, "network_pending", "", 0)
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	var n int
	w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs`).Scan(&n)
	if n != 0 || len(m.active) != 0 {
		t.Fatal("another user's credentials were enrolled", n)
	}
	sources, e := metadataSourceStatus(w.libraryDB, w.db, ServeOptions{Settings: w.settings, PluginRegistry: w.registry}, adminCtx(1))
	if e != nil || len(sources) != 0 {
		t.Fatal("private connection exposed", sources, e)
	}
}

func TestSourceCooldownUsesCompletionAndNeverShortensConfiguredInterval(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	now := time.Unix(1800000000, 250000000)
	m.now = func() time.Time { return now }
	w.libraryDB.Exec(`INSERT INTO metadata_source_limits(connection_id,interval_seconds,next_at) VALUES(1,3600,0)`)
	if e := m.coolSource(1); e != nil {
		t.Fatal(e)
	}
	var next, last int64
	w.libraryDB.QueryRow(`SELECT next_at,last_request_at FROM metadata_source_limits WHERE connection_id=1`).Scan(&next, &last)
	if next != 1800003601 || last != 1800000001 {
		t.Fatal("fractional clock weakened request spacing", next, last)
	}
	if sourceClock(time.Unix(1800000000, 0)) != 1800000000 {
		t.Fatal("exact seconds must not gain an extra second")
	}
}

func TestSourceCompletedPartialRemainsTerminalAfterExplicitFileRefresh(t *testing.T) {
	w, _, file := referenceFixture(t)
	m := testMetadataWorker(t, w)
	os.WriteFile(file, []byte("作者：Selected author"), 0600)
	if e := m.local(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	m.saveJob(1, "partial", "所有来源已处理，信息仍有缺项", 0)
	w.libraryDB.Exec(`INSERT INTO metadata_source_jobs(book_id,connection_id,phase,matched) VALUES(1,1,'done',1)`)
	if e := m.local(context.Background(), 1, true); e != nil {
		t.Fatal(e)
	}
	var status string
	w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=1`).Scan(&status)
	if status != "partial" {
		t.Fatal("explicit lookup created a phantom pending job", status)
	}
}

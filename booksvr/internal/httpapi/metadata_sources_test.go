package httpapi

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginruntime"
	"testing"
	"time"
)

// Drive the real coordinator through persisted phases, without real rate-limit
// sleeps. Failed requests remain queued for a later invocation.
func runMetadataNetwork(t *testing.T, m *metadataWorker) error {
	t.Helper()
	for phase := 0; phase < 8; phase++ {
		if e := m.network(context.Background()); e != nil {
			return e
		}
		for len(m.active) > 0 {
			select {
			case r := <-m.results:
				delete(m.active, r.task.connection)
				settings := m.w.settings
				if settingBool(settings, "auto_fill_meta", false) {
					if e := m.finishSource(r, settings); e != nil {
						return e
					}
				}
			case <-time.After(15 * time.Second):
				t.Fatal("network coordinator did not finish")
			}
		}
		var active, failed int
		if e := m.w.libraryDB.QueryRow(`SELECT count(*),coalesce(sum(attempts),0) FROM metadata_source_jobs WHERE phase<>'done'`).Scan(&active, &failed); e != nil {
			return e
		}
		if active == 0 || failed > 0 || !settingBool(m.w.settings, "auto_fill_meta", false) {
			return nil
		}
		next := m.now().Add(5 * time.Second)
		m.now = func() time.Time { return next }
	}
	t.Fatal("source phases failed to drain")
	return nil
}

type concurrentMetadataProvider struct {
	*referenceProvider
	started chan string
	gate    chan struct{}
	calls   atomic.Int32
	empty   bool
}

func (p *concurrentMetadataProvider) Invoke(method string, args []any, ctx map[string]any) (any, error) {
	return pluginbuiltin.DispatchInvoke(p, method, args, ctx)
}
func (p *concurrentMetadataProvider) SearchBooks(q plugindomain.MetadataQuery, ctx map[string]any) ([]plugindomain.BookMetadata, error) {
	p.calls.Add(1)
	if p.started != nil {
		p.started <- p.key
	}
	if p.gate != nil {
		<-p.gate
	}
	if p.empty {
		return nil, nil
	}
	return p.referenceProvider.SearchBooks(q, ctx)
}
func registerConcurrentMetadata(t *testing.T, w *bookWiring, p *concurrentMetadataProvider) {
	t.Helper()
	if _, e := w.registry.Register(p, pluginiface.CapabilitySet{Interfaces: map[string]bool{"MetadataProvider": true}, HasSelfCheck: true}); e != nil {
		t.Fatal(e)
	}
}
func drainSourceResults(t *testing.T, m *metadataWorker) {
	t.Helper()
	for len(m.results) > 0 {
		r := <-m.results
		delete(m.active, r.task.connection)
		if e := m.finishSource(r, m.w.settings); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSourceConcurrencySlowIsolationAndMoreThanTwoSources(t *testing.T) {
	w, _, file := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	started := make(chan string, 10)
	gate := make(chan struct{})
	slow := &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: "test.slow"}, started: started, gate: gate, empty: true}
	fast := &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: "test.fast"}, started: started, empty: true}
	third := &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: "test.third"}, started: started, empty: true}
	for _, p := range []*concurrentMetadataProvider{slow, fast, third} {
		registerConcurrentMetadata(t, w, p)
	}
	if _, e := pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry); e != nil {
		t.Fatal(e)
	}
	w.db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE plugin_key='talebook.test.reference'`)
	os.WriteFile(file, []byte("作者：Selected author"), 0600)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	if e := m.local(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(gate)
			t.Fatal("all three sources must start before slow source finishes")
		}
	}
	// Fast results finish and can advance to another book while the slow source is blocked.
	deadline := time.After(3 * time.Second)
	for len(m.results) < 2 {
		select {
		case <-deadline:
			close(gate)
			t.Fatal("fast source blocked by slow source")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	drainSourceResults(t, m)
	insertBook(t, w.libraryDB, 2, "Selected title")
	insertItem(t, w.db, 2, "public", 1)
	if e := w.updateBookMeta(2, "Selected title", map[string]any{"authors": []string{"Selected author"}}); e != nil {
		t.Fatal(e)
	}
	if e := m.saveJob(2, "network_pending", "", 0); e != nil {
		t.Fatal(e)
	}
	now := m.now().Add(time.Second)
	m.now = func() time.Time { return now }
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	if fast.calls.Load() != 1 {
		close(gate)
		t.Fatal("source interval bypassed")
	}
	now = now.Add(4 * time.Second)
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(gate)
			t.Fatal("fast sources did not advance independently")
		}
	}
	if fast.calls.Load() != 2 || slow.calls.Load() != 1 {
		close(gate)
		t.Fatal(fast.calls.Load(), slow.calls.Load())
	}
	close(gate)
	for len(m.active) > 0 {
		select {
		case r := <-m.results:
			delete(m.active, r.task.connection)
			if e := m.finishSource(r, w.settings); e != nil {
				t.Fatal(e)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("source completion stuck")
		}
	}
	var cached int
	w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_source_jobs WHERE phase='done' AND record NOT IN ('null','{}','')`).Scan(&cached)
	if cached != 0 {
		t.Fatal("terminal queue retains response payloads")
	}
}
func TestSourceRateSurvivesWorkerRestart(t *testing.T) {
	w, _, file := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	os.WriteFile(file, []byte("作者：Selected author"), 0600)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
	if e := m.local(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	r := <-m.results
	delete(m.active, r.task.connection)
	if e := m.finishSource(r, w.settings); e != nil {
		t.Fatal(e)
	}
	m2 := testMetadataWorker(t, w)
	now := m.now().Add(time.Second)
	m2.now = func() time.Time { return now }
	if e := m2.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(m2.active) != 0 {
		t.Fatal("restart bypassed persisted source rate")
	}
	var requests int
	w.libraryDB.QueryRow(`SELECT requests FROM metadata_source_limits`).Scan(&requests)
	if requests != 1 {
		t.Fatal(requests)
	}
	now = now.Add(4 * time.Second)
	if e := runMetadataNetwork(t, m2); e != nil {
		t.Fatal(e)
	}
	w.libraryDB.QueryRow(`SELECT requests FROM metadata_source_limits`).Scan(&requests)
	if requests != 2 {
		t.Fatal("search and details must each consume rate budget", requests)
	}
}
func TestGuardedNetworkFillRejectsConcurrentIdentityChanges(t *testing.T) {
	w, _, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	identity, e := library.MetadataIdentity(w.libraryDB, 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.libraryDB.Exec(`UPDATE books SET title='A different manually edited book' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	fields, e := library.FillMissingMetadataGuarded(w.libraryDB, 1, metadataCandidate(map[string]any{"publisher": "wrong press", "comments": "wrong description"}), identity, "network:test")
	if e != nil || len(fields) != 0 {
		t.Fatal(fields, e)
	}
	b, _ := bookGetOne(w.Config(""), 1)
	if b.Publisher != "" || b.Comments != "" {
		t.Fatal("stale response mixed metadata", b)
	}
}

func TestSourceGlobalConcurrencyBound(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	gate := make(chan struct{})
	started := make(chan string, 9)
	for i := 0; i < 9; i++ {
		registerConcurrentMetadata(t, w, &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: fmt.Sprintf("test.cap%d", i)}, started: started, gate: gate, empty: true})
	}
	pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
	w.db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE plugin_key='talebook.test.reference'`)
	w.libraryDB.Exec(`UPDATE books SET title='Selected title' WHERE id=1`)
	if e := w.updateBookMeta(1, "Selected title", map[string]any{"authors": []string{"Selected author"}}); e != nil {
		t.Fatal(e)
	}
	m.saveJob(1, "network_pending", "", 0)
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(gate)
			t.Fatal("concurrent sources did not start")
		}
	}
	if len(m.active) != 8 || len(started) != 0 {
		close(gate)
		t.Fatal("global concurrency bound bypassed")
	}
	close(gate)
	for len(m.active) > 0 {
		select {
		case r := <-m.results:
			delete(m.active, r.task.connection)
			if e := m.finishSource(r, w.settings); e != nil {
				t.Fatal(e)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("source completion stuck")
		}
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("ninth source starved")
	}
	for len(m.active) > 0 {
		r := <-m.results
		delete(m.active, r.task.connection)
		if e := m.finishSource(r, w.settings); e != nil {
			t.Fatal(e)
		}
	}
}

func TestSourceNewArrivalPrecedesHistoricalQueue(t *testing.T) {
	w, _, _ := referenceFixture(t)
	m := testMetadataWorker(t, w)
	w.settings["auto_fill_meta"] = true
	p := &concurrentMetadataProvider{referenceProvider: &referenceProvider{key: "test.priority"}, empty: true}
	registerConcurrentMetadata(t, w, p)
	pluginruntime.EnsureRuntimeInstallations(w.db, w.settings, w.registry)
	w.db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE plugin_key='talebook.test.reference'`)
	m.setState("initial_max", 1)
	insertBook(t, w.libraryDB, 2, "Selected title")
	insertItem(t, w.db, 2, "public", 1)
	for _, id := range []int64{1, 2} {
		if e := w.updateBookMeta(id, "Selected title", map[string]any{"authors": []string{"Selected author"}}); e != nil {
			t.Fatal(e)
		}
		m.saveJob(id, "network_pending", "", 0)
	}
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	r := <-m.results
	delete(m.active, r.task.connection)
	if r.task.book != 2 {
		t.Fatal("new arrival waits behind history", r.task.book)
	}
	if e := m.finishSource(r, w.settings); e != nil {
		t.Fatal(e)
	}
	now := m.now().Add(5 * time.Second)
	m.now = func() time.Time { return now }
	if e := m.network(context.Background()); e != nil {
		t.Fatal(e)
	}
	r = <-m.results
	delete(m.active, r.task.connection)
	if r.task.book != 1 {
		t.Fatal("history failed to resume", r.task.book)
	}
	if e := m.finishSource(r, w.settings); e != nil {
		t.Fatal(e)
	}
}

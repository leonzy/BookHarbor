package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

// Explicit opt-in, read-only probe. Ordinary tests never need production data.
// Run only in an isolated container with the database mounted read-only.
func TestMemoryProbeSourceBaseline(t *testing.T) {
	path := os.Getenv("BOOKHARBOR_MEMORY_PROBE_DB")
	if path == "" {
		t.Skip("read-only diagnostic database not supplied")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runtime.GC()
	var before, loaded, prepared runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	tracked, err := loadSourceTracked(ctx, db, SourceSyncOptions{}, "/data/books/library")
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&loaded)
	// The same preparation as the production full-discovery baseline.
	known, cached, transferred := prepareSourceFiles(tracked, SourceSyncOptions{FullDiscovery: true}, true)
	runtime.ReadMemStats(&prepared)
	if out := os.Getenv("BOOKHARBOR_MEMORY_PROBE_PROFILE"); out != "" {
		runtime.GC()
		f, e := os.Create(out)
		if e != nil {
			t.Fatal(e)
		}
		e = pprof.WriteHeapProfile(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	var live runtime.MemStats
	runtime.ReadMemStats(&live)
	data := map[string]any{"records": len(tracked), "physical_paths": len(known), "cached": len(cached), "duration_seconds": time.Since(start).Seconds(), "loader_alloc_bytes": loaded.TotalAlloc - before.TotalAlloc, "preparation_alloc_bytes": prepared.TotalAlloc - loaded.TotalAlloc, "heap_before": before.HeapAlloc, "heap_loaded": loaded.HeapAlloc, "heap_prepared": prepared.HeapAlloc, "heap_live": live.HeapAlloc}
	b, _ := json.Marshal(data)
	t.Log(string(b))
	runtime.KeepAlive(tracked)
	runtime.KeepAlive(known)
	runtime.KeepAlive(transferred)
	runtime.KeepAlive(cached)
}

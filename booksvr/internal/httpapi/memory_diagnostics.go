package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"
)

// Private, opt-in files only: no listener, no public pprof endpoint, no forced GC.
// One JSON and one heap profile are replaced atomically; disk use is bounded.
func startMemoryDiagnostics(parent context.Context, dir string, books, users *sql.DB, out io.Writer) (func(), error) {
	if dir == "" {
		return func() {}, nil
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("memory diagnostics requires an absolute private directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("memory diagnostics directory must not be a symlink")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		ticks := 0
		for {
			if err := writeMemoryDiagnostics(dir, books, users, ticks > 0 && ticks%4 == 0); err != nil && out != nil {
				fmt.Fprintf(out, "memory diagnostics: %v\n", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ticks++
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func privateDiagnosticFile(dir, name string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(dir, ".memory-")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = f.Chmod(0600); err == nil {
		err = write(f)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporary, filepath.Join(dir, name))
}

func writeMemoryDiagnostics(dir string, books, users *sql.DB, heap bool) error {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	sample := map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "pid": os.Getpid(), "heap_alloc": memory.HeapAlloc, "heap_inuse": memory.HeapInuse, "heap_idle": memory.HeapIdle, "heap_released": memory.HeapReleased, "total_alloc": memory.TotalAlloc, "num_gc": memory.NumGC, "last_gc": memory.LastGC, "stack_inuse": memory.StackInuse, "goroutines": runtime.NumGoroutine()}
	if books != nil {
		sample["library_connections"] = books.Stats()
	}
	if users != nil {
		sample["user_connections"] = users.Stats()
	}
	if err := privateDiagnosticFile(dir, "memory.json", func(w io.Writer) error { return json.NewEncoder(w).Encode(sample) }); err != nil {
		return err
	}
	if heap {
		return privateDiagnosticFile(dir, "heap.pprof", pprof.WriteHeapProfile)
	}
	return nil
}

package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMemoryDiagnosticsPrivateBoundedFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := writeMemoryDiagnostics(dir, nil, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			t.Fatal(e)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
	blob, e := os.ReadFile(filepath.Join(dir, "memory.json"))
	if e != nil {
		t.Fatal(e)
	}
	var sample map[string]any
	if e = json.Unmarshal(blob, &sample); e != nil || sample["pid"] != float64(os.Getpid()) || sample["heap_alloc"] == nil {
		t.Fatal(sample, e)
	}
	blob, e = os.ReadFile(filepath.Join(dir, "heap.pprof"))
	if e != nil || len(blob) < 2 || blob[0] != 0x1f || blob[1] != 0x8b {
		t.Fatal("invalid gzip profile", e)
	}
}

func TestMemoryDiagnosticsOptInAndCancellation(t *testing.T) {
	stop, e := startMemoryDiagnostics(context.Background(), "", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	stop()
	if _, e = startMemoryDiagnostics(context.Background(), "relative", nil, nil, nil); e == nil {
		t.Fatal("relative path accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop, e = startMemoryDiagnostics(ctx, t.TempDir(), nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	stop()
}

func TestMemoryDiagnosticsRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges unavailable in Windows fixture")
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "alias")
	if e := os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	if _, e := startMemoryDiagnostics(context.Background(), link, nil, nil, nil); e == nil {
		t.Fatal("symlink accepted")
	}
}

package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanRunExplicitScopeAndFullMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new-collection")
	os.Mkdir(target, 0700)
	stamp := time.Unix(1700000000, 0)
	if e := os.Chtimes(target, stamp, stamp); e != nil {
		t.Fatal(e)
	}
	cfg := scanTestConfig(t, scanTestDB(t), root)
	var incremental, full []string
	cfg.StartScan = func(path string, _ int) error { incremental = append(incremental, path); return nil }
	cfg.StartFullScan = func(path string, _ int) error { full = append(full, path); return nil }
	request := func(body map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		ctx, _ := scanCtx(t, http.MethodPost, rScanRun, string(raw), true)
		rsp, e := ScanRunHandler(cfg)(ctx)
		if e != nil {
			t.Fatal(e)
		}
		return rsp
	}
	if r := request(map[string]any{"path": target}); r["err"] != "ok" || len(incremental) != 1 || incremental[0] != target || len(full) != 0 {
		t.Fatal(r, incremental, full)
	}
	if r := request(map[string]any{"path": target, "mode": "full"}); r["err"] != "ok" || len(full) != 1 || full[0] != target {
		t.Fatal(r, full)
	}
	if r := request(map[string]any{"path": t.TempDir()}); r["err"] != "params.path" {
		t.Fatal(r)
	}
	if r := request(map[string]any{"mode": "surprise"}); r["err"] != "params.invalid" {
		t.Fatal(r)
	}
	info, e := os.Stat(target)
	if e != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("scan preflight changed the directory stamp: %v %v", info, e)
	}
	if len(incremental) != 1 || len(full) != 1 {
		t.Fatal("invalid requests started work")
	}
}

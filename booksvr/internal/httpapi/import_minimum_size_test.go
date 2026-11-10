package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestImportMinimumSizeSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.py")
	root := t.TempDir()
	cfg := importTestConfig(t, root)
	cfg.SettingsPath = path
	cfg.SaveSettings = func(v map[string]any) error { return SaveSettings(path, v) }
	if e := SaveSettings(path, map[string]any{"scan_upload_path": root, "import_allowed_roots": []string{root}}); e != nil {
		t.Fatal(e)
	}
	reader := NewImportMinimumSizeReader(path)
	if reader() != 10240 {
		t.Fatal(reader())
	}
	for _, body := range []string{`{"min_file_size_kib":0}`, `{"min_file_size_kib":24}`, `{"auto_watch_enabled":false}`} {
		ctx, _ := importCtx(t, http.MethodPost, rImportSettings, body, true)
		rsp, e := ImportSettingsHandler(cfg)(ctx)
		if e != nil || rsp["err"] != "ok" {
			t.Fatal(rsp, e)
		}
	}
	if reader() != 24*1024 {
		t.Fatal("cached scan policy not invalidated", reader())
	}
	generation := settingString(LoadSettings(path), "import_size_audit_requested")
	if generation == "" {
		t.Fatal("explicit save did not request existing-library check")
	}
	if e := SaveSettings(path, map[string]any{"import_min_file_size_kib": 24, "site_title": "test"}); e != nil {
		t.Fatal(e)
	}
	if settingString(LoadSettings(path), "import_size_audit_requested") != generation {
		t.Fatal("unrelated settings save restarted cleanup")
	}
	ctx, _ := importCtx(t, http.MethodGet, rImportSettings, "", true)
	rsp, e := ImportSettingsHandler(cfg)(ctx)
	if e != nil || rsp["settings"].(map[string]any)["min_file_size_kib"] != int64(24) {
		t.Fatal(rsp, e)
	}
	for _, bad := range []string{`-1`, `1.5`, `true`, `null`, `"abc"`, `2147483648`} {
		ctx, _ := importCtx(t, http.MethodPost, rImportSettings, `{"min_file_size_kib":`+bad+`}`, true)
		rsp, e = ImportSettingsHandler(cfg)(ctx)
		if e != nil || rsp["err"] != "params.min_file_size" {
			t.Fatal(bad, rsp, e)
		}
	}
	if reader() != 24*1024 {
		t.Fatal("invalid save changed policy")
	}
	if e := SaveSettings(path, map[string]any{"import_min_file_size_kib": -1}); e == nil {
		t.Fatal("generic settings bypassed validation")
	}
	if e := SaveSettings(path, map[string]any{"import_min_file_size_kib": 0}); e != nil || reader() != 0 {
		t.Fatal("zero must disable", e, reader())
	}
}

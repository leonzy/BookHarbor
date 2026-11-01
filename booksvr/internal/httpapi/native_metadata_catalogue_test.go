package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
	"testing"
)

func TestMetadataOnlyCatalogueUsesNativeSourcesAndProtectsControls(t *testing.T) {
	w, _, _ := referenceFixture(t)
	testMetadataWorker(t, w)
	reg := pluginruntime.NewPluginRegistry()
	if e := pluginbuiltin.Register(reg); e != nil {
		t.Fatal(e)
	}
	d, e := newMetadataOnlyDispatcher(ServeOptions{Addr: "127.0.0.1:8004", Settings: w.settings, PluginRegistry: reg}, w.libraryDB, w.db)
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/api/admin/plugins", "/api/admin/plugins/connections", "/api/admin/plugins/installations/1/state", "/api/admin/plugins/connections/1/test"} {
		r := httptest.NewRecorder()
		d.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(r.Body.String(), "user.need_login") {
			t.Fatalf("guest %s %s", path, r.Body.String())
		}
	}
	d.server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: 1, IsAdmin: true}, nil }
	r := httptest.NewRecorder()
	d.ServeHTTP(r, httptest.NewRequest("GET", "/api/admin/plugins", nil))
	var result struct {
		Err         string `json:"err"`
		Definitions []struct {
			Key string `json:"plugin_key"`
		} `json:"definitions"`
	}
	if e = json.Unmarshal(r.Body.Bytes(), &result); e != nil || result.Err != "ok" {
		t.Fatalf("catalogue %s %v", r.Body.String(), e)
	}
	keys := map[string]bool{}
	for _, v := range result.Definitions {
		keys[v.Key] = true
	}
	if len(keys) != 40 || !keys["bookharbor.meta.google-books"] || !keys["bookharbor.meta.amazon"] || keys["talebook.meta.calibre"] {
		t.Fatalf("native catalogue %v", keys)
	}
}

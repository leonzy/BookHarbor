package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginruntime"
)

type wereadConnectProvider struct {
	*pluginbuiltin.WereadProvider
	t     *testing.T
	calls int
}

func (p *wereadConnectProvider) Invoke(method string, args []any, ctx map[string]any) (any, error) {
	p.calls++
	if method != "execute_feature" || args[0] != "notebooks" || args[1].(map[string]any)["count"] != float64(1) || ctx["secrets"].(map[string]any)["api_key"] != "fixture-private-key" {
		p.t.Fatal("incorrect feature dispatch or decrypted credentials")
	}
	return map[string]any{"books": []any{}}, nil
}

func TestWereadHTTPConnectSavesAndReusesPrivateCredentials(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	reg := pluginruntime.NewPluginRegistry()
	if err := pluginbuiltin.Register(reg); err != nil {
		t.Fatal(err)
	}
	actual, err := reg.Get("talebook.combo.weread")
	if err != nil {
		t.Fatal(err)
	}
	p := &wereadConnectProvider{WereadProvider: actual.(*pluginbuiltin.WereadProvider), t: t}
	reg = pluginruntime.NewPluginRegistry()
	if _, err := reg.Register(p, pluginiface.CapabilitySet{Interfaces: map[string]bool{"MetadataProvider": true, "AnnotationProvider": true, "ExtraFeatureProvider": true}, HasExecute: true}); err != nil {
		t.Fatal(err)
	}
	fx.Cfg.Registry = reg
	for _, body := range []string{`{"params":{"count":1},"credentials":{"api_key":"fixture-private-key"}}`, `{"params":{"count":1}}`} {
		ctx := pluginCtx(t, fx.DB, http.MethodPost, "/", body, 2, false)
		ctx.Params = []string{"talebook.combo.weread", "notebooks"}
		got, err := UserPluginFeatureHandler(fx.Cfg)(ctx)
		if err != nil || got["err"] != "ok" {
			t.Fatalf("connect/reuse failed: %+v %v", got, err)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "fixture-private-key") {
			t.Fatal("credentials exposed in response")
		}
	}
	if p.calls != 2 {
		t.Fatalf("dispatch calls: %d", p.calls)
	}
	ctx := pluginCtx(t, fx.DB, http.MethodPost, "/", `{"params":{"count":1}}`, 1, true)
	ctx.Params = []string{"talebook.combo.weread", "notebooks"}
	got, err := UserPluginFeatureHandler(fx.Cfg)(ctx)
	if err != nil || got["err"] != "plugin.connection_missing" || p.calls != 2 {
		t.Fatalf("cross-user reuse: %+v %v", got, err)
	}
}

func TestPluginFeaturesOnlyPreservesDatabaseAndWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sentinel(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	opts := ServeOptions{PluginFeaturesOnly: true, Addr: "127.0.0.1:0", CookieSecret: "test", DBPath: path, TornadoURL: "http://127.0.0.1:1"}
	opts.ServiceFactory = func(*sql.DB, *sql.DB) ServiceSet { t.Fatal("background factory called"); return ServiceSet{} }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, opts); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("schema mutated: %d %v", count, err)
	}
	for _, addr := range []string{"0.0.0.0:8003", ":8003", "192.168.1.96:8003"} {
		opts.Addr = addr
		if _, err := newPluginFeaturesOnlyDispatcher(opts, db, db); err == nil {
			t.Fatalf("public listener allowed: %s", addr)
		}
	}
	opts.Addr = "127.0.0.1:0"
	opts.ReaderOnly = true
	if _, err := newPluginFeaturesOnlyDispatcher(opts, db, db); err == nil {
		t.Fatal("conflicting modes accepted")
	}
}

func TestPluginFeaturesOnlyRequiresLogin(t *testing.T) {
	opts := ServeOptions{Addr: "127.0.0.1:0", CookieSecret: "test", TornadoURL: "http://127.0.0.1:1"}
	d, err := newPluginFeaturesOnlyDispatcher(opts, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	d.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/plugins/talebook.combo.weread/features/notebooks", strings.NewReader(`{"params":{"count":1}}`)))
	if !strings.Contains(r.Body.String(), "user.need_login") {
		t.Fatalf("guest response: %d %s", r.Code, r.Body.String())
	}
}

// The real manifest must pass the actual HTTP gate. An invalid credential shape
// stops before the external network and proves that count was not rejected first.
func TestWereadHTTPConnectSchema(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	reg := pluginruntime.NewPluginRegistry()
	if err := pluginbuiltin.Register(reg); err != nil {
		t.Fatal(err)
	}
	fx.Cfg.Registry = reg
	for _, tc := range []struct{ body, want string }{
		{`{"params":{"count":1},"credentials":true}`, "plugin.credentials_invalid"},
		{`{"params":{"count":"1"},"credentials":true}`, "feature.type_invalid"},
		{`{"params":{"unknown":1},"credentials":true}`, "feature.unknown_field"},
	} {
		ctx := pluginCtx(t, fx.DB, http.MethodPost, "/", tc.body, 2, false)
		ctx.Params = []string{"talebook.combo.weread", "notebooks"}
		got, err := UserPluginFeatureHandler(fx.Cfg)(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got["err"] != tc.want {
			t.Fatalf("want %s: %+v", tc.want, got)
		}
	}
}

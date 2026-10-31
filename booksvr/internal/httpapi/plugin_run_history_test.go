package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPluginRunHistoryMaintenanceRoutes(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	createCalibreTables(t, fx.DB)
	cid := pluginSeedInstanceConnection(t, fx)
	if _, err := fx.DB.Exec(`UPDATE plugin_connections SET owner_type='user',owner_id=1 WHERE id=?`, cid); err != nil {
		t.Fatal(err)
	}
	id := pluginSeedRun(t, fx, cid, "run", "failed")
	d, err := newMetadataOnlyDispatcher(ServeOptions{Addr: "127.0.0.1:8004", Settings: fx.Cfg.Settings}, fx.DB, fx.DB)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{UserID: 1, IsAdmin: true}
	d.server.Identify = func(*http.Request) (Identity, error) { return identity, nil }
	for _, route := range []string{"/api/admin/plugins/runs", "/api/admin/plugins/runs/" + jsonNumber(id), "/api/plugins/runs", "/api/plugins/runs/" + jsonNumber(id)} {
		for _, role := range []string{"owner-admin", "owner-user", "other-user", "guest"} {
			identity = Identity{UserID: 1, IsAdmin: role == "owner-admin"}
			if role == "other-user" {
				identity.UserID = 2
			} else if role == "guest" {
				identity = Identity{}
			}
			r := httptest.NewRecorder()
			d.ServeHTTP(r, httptest.NewRequest("GET", route, nil))
			var out map[string]any
			if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
				t.Fatal(route, role, r.Code, r.Body.String())
			}
			wantOK := role == "owner-admin" || (role == "owner-user" && !strings.HasPrefix(route, "/api/admin/")) || (role == "other-user" && route == "/api/plugins/runs")
			if (out["err"] == "ok") != wantOK {
				t.Fatal(route, role, out)
			}
		}
	}
}

func TestPluginRunHistoryOwnerScopes(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	var ids []int64
	owners := []int64{1, 2, 0}
	for _, owner := range owners {
		cid := pluginSeedInstanceConnection(t, fx)
		if owner != 0 {
			if _, err := fx.DB.Exec(`UPDATE plugin_connections SET owner_type='user',owner_id=? WHERE id=?`, owner, cid); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, pluginSeedRun(t, fx, cid, "run", "failed"))
	}
	for _, admin := range []bool{false, true} {
		ctx := pluginCtx(t, fx.DB, http.MethodGet, "/", "", 1, admin)
		list, detail := UserPluginRunsHandler(fx.Cfg), UserPluginRunDetailHandler(fx.Cfg)
		wantList := 1
		if admin {
			list, detail = AdminPluginRunsHandler(fx.Cfg), AdminPluginRunDetailHandler(fx.Cfg)
			wantList = 2
		}
		out, err := list(ctx)
		if err != nil || len(out["runs"].([]map[string]any)) != wantList {
			t.Fatalf("admin=%v list=%v error=%v", admin, out, err)
		}
		for i, id := range ids {
			ctx.Params = []string{jsonNumber(id)}
			out, err := detail(ctx)
			want := "plugin.run_missing"
			if owners[i] == 1 || (admin && owners[i] == 0) {
				want = "ok"
			}
			if err != nil || out["err"] != want {
				t.Fatalf("admin=%v owner=%v detail=%v error=%v", admin, i, out, err)
			}
			if want == "ok" && out["run"].(map[string]any)["can_manage"] != (owners[i] == 0) {
				t.Fatal("personal records must remain read-only", out)
			}
		}
	}
}

func TestPluginRunHistoryBackgroundRecordsRemainReadOnly(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	cid := pluginSeedInstanceConnection(t, fx)
	id := pluginSeedRun(t, fx, cid, "run", "failed")
	if _, err := fx.DB.Exec(`UPDATE plugin_runs SET input_data='{"background_metadata":true,"private":"never public"}' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/", "", 1, true)
	ctx.Params = []string{jsonNumber(id)}
	out, err := AdminPluginRunDetailHandler(fx.Cfg)(ctx)
	if err != nil || out["err"] != "ok" {
		t.Fatal(out, err)
	}
	run := out["run"].(map[string]any)
	if run["can_manage"] != false || run["input_data"] != nil {
		t.Fatal("background records must not expose manual actions or private input", run)
	}
}

func TestPluginRunHistoryOlderThanThousand(t *testing.T) {
	fx := newPluginFixture(t, "talebook.test.plugin", nil)
	cid := pluginSeedInstanceConnection(t, fx)
	first := pluginSeedRun(t, fx, cid, "run", "failed")
	_, err := fx.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1001) INSERT INTO plugin_runs(connection_id,action,"trigger",status,requested_by,attempt,counts,cursor_before,cursor_after,input_data,error_code,error_message,create_time,duration_ms) SELECT ?,'run','manual','succeeded',1,1,'{}','{}','{}','{}','','','2026-01-01 00:00:00',0 FROM n`, cid)
	if err != nil {
		t.Fatal(err)
	}
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/", "", 1, true)
	ctx.Params = []string{jsonNumber(first)}
	out, err := AdminPluginRunDetailHandler(fx.Cfg)(ctx)
	if err != nil || out["err"] != "ok" || out["run"].(map[string]any)["id"] != first {
		t.Fatal(out, err)
	}
}

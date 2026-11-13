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

	_ "modernc.org/sqlite"
	"talebook/booksvr/internal/access"
	"talebook/booksvr/internal/library"
)

func TestAccessAdminDraftRequiresAdminAndCannotPublish(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "acl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	store := library.AccessStore{DB: db}
	s, err := NewServer(AccessAdminRouteSpecs(AccessAdminConfig{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{}
	s.Identify = func(*http.Request) (Identity, error) { return identity, nil }
	call := func(method, path, body string) map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		var result map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err, rr.Body.String())
		}
		return result
	}
	body := `{"version":0,"base_revision":0,"policy":{}}`
	if got := call("POST", "/api/admin/access/revision", body)["err"]; got != ErrNeedLogin {
		t.Fatal(got)
	}
	identity = Identity{UserID: 2}
	if got := call("POST", "/api/admin/access/revision", body)["err"]; got != ErrNotAdmin {
		t.Fatal(got)
	}
	identity = Identity{UserID: 1, IsAdmin: true}
	if got := call("POST", "/api/admin/access/revision", body+` {}`)["err"]; got != "params.invalid" {
		t.Fatal(got)
	}
	if got := call("POST", "/api/admin/access/revision", `{"version":0,"base_revision":0,"profile":"fake","policy":{}}`)["err"]; got != "params.invalid" {
		t.Fatal(got)
	}
	created := call("POST", "/api/admin/access/revision", body)
	if created["err"] != "ok" {
		t.Fatal(created)
	}
	r := created["revision"].(map[string]any)
	if r["status"] != "draft" {
		t.Fatal(r)
	}
	var active int
	if err = db.QueryRow(`SELECT active_revision FROM acl_state`).Scan(&active); err != nil || active != 0 {
		t.Fatal(active, err)
	}
	updated := call("PUT", "/api/admin/access/revision/1", `{"version":1,"base_revision":0,"policy":{}}`)
	if updated["err"] != "ok" {
		t.Fatal(updated)
	}
	if got := call("PUT", "/api/admin/access/revision/1", `{"version":1,"base_revision":0,"policy":{}}`)["err"]; got != "access.conflict" {
		t.Fatal(got)
	}
	if got := call("GET", "/api/admin/access/revision/1/preview?principal=fake", "")["err"]; got != "access.not_ready" {
		t.Fatal(got)
	}
	actor, err := store.RegisterPrincipal(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BeginPreparation(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	token := library.AccessBuildToken{Revision: 1, Version: 2}
	if _, err = store.BuildAccessProfiles(context.Background(), token, []string{actor}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO acl_build(revision,version,phase) VALUES(1,2,'delta')`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	preview := call("GET", "/api/admin/access/revision/1/preview?principal="+actor, "")
	if preview["err"] != "ok" || preview["read_only"] != true || preview["count_kind"] != "edition_records" {
		t.Fatal(preview)
	}
	if got := call("POST", "/api/admin/access/revision/1/apply", `{"version":2,"watermark":0}`)["err"]; got != "access.runtime_not_ready" {
		t.Fatal("incomplete runtime published rules", got)
	}
	if got := call("POST", "/api/admin/access/revision/1/cancel", `{"version":1}`)["err"]; got != "access.conflict" {
		t.Fatal("stale cancel accepted", got)
	}
	if got := call("POST", "/api/admin/access/revision/1/cancel", `{"version":2}`)["err"]; got != "ok" {
		t.Fatal("cancel failed", got)
	}
	if got := call("POST", "/api/admin/access/revision/1/prepare", `{"version":2}`)["err"]; got != "access.conflict" {
		t.Fatal("old worker restarted", got)
	}
	if got := call("POST", "/api/admin/access/revision/1/prepare", `{"version":3}`)["err"]; got != "ok" {
		t.Fatal("restart failed", got)
	}
	identity = Identity{UserID: 2}
	if got := call("GET", "/api/admin/access/revision/1/preview?principal="+actor, "")["err"]; got != ErrNotAdmin {
		t.Fatal("reader impersonated another user", got)
	}
}

func TestAccessPublishResponseUsesNewGeneration(t *testing.T) {
	db := netSchemaDB(t)
	if err := library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	c := context.Background()
	store := library.AccessStore{DB: db}
	actor, err := store.RegisterPrincipal(c, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.SaveDraft(c, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BeginPreparation(c, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := library.AccessBuildToken{Revision: r.ID, Version: r.Version}
	if _, err = store.BuildAccessProfiles(c, token, []string{actor}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO acl_build(revision,version,phase) VALUES(1,1,'delta')`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishPreparation(c, token); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(AccessAdminRouteSpecs(AccessAdminConfig{Store: store, CheckRuntime: func(context.Context) error { return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: 1, IsAdmin: true}, nil }
	server.PrepareAccess = func(ctx *Context) (func() error, error) {
		scope, e := store.ResolveAccess(c, ctx.Identity.UserID, true)
		if e != nil {
			return nil, e
		}
		ctx.Access = &scope
		return func() error { return store.AccessCurrent(c, scope) }, nil
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("POST", "/api/admin/access/revision/1/apply", strings.NewReader(`{"version":1,"watermark":0}`)))
	var response map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response["err"] != "ok" {
		t.Fatal("successful publish looked like a failed request", w.Code, w.Body.String(), err)
	}
}

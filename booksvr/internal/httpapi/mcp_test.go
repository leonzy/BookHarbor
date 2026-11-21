package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

func mcpHTTPFixture(t *testing.T) (*sql.DB, *mcp.Service, *pluginruntime.Runtime, *userdb.PluginInstallation) {
	t.Helper()
	db := netSchemaDB(t)
	if _, err := db.Exec(`INSERT INTO readers(id,username,admin,active,permission,create_time) VALUES(1,'one',0,1,'rps','2026-01-01'),(2,'two',0,1,'rps','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	registry := pluginruntime.NewPluginRegistry()
	if err := pluginbuiltin.Register(registry); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"PLUGIN_SECRET_KEY": "mcp-test-secret"}
	if _, err := pluginruntime.EnsureBuiltinInstallations(db, 1, settings, registry); err != nil {
		t.Fatal(err)
	}
	installation, err := userdb.GetPluginInstallationByKey(db, mcp.PluginID)
	if err != nil || installation == nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE plugin_installations SET enabled=1,status='active' WHERE id=?`, installation.ID); err != nil {
		t.Fatal(err)
	}
	runtime := pluginruntime.NewRuntime(db, settings, registry)
	service := mcp.NewService(db, db, settings, registry)
	return db, service, runtime, installation
}

func mcpRequest(t *testing.T, handler http.Handler, token, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	request.RemoteAddr = remoteAddr
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestMCPBearerRemoteAndDisabledPlugin(t *testing.T) {
	db, service, runtime, installation := mcpHTTPFixture(t)
	token := "0123456789abcdef0123456789abcdef"
	if _, err := runtime.SaveConnection(installation.ID, "user", 1, map[string]any{"access_token": token}, "agent", map[string]any{"allow_remote": true}, nil, "", "agent"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(MCPRouteSpecs(MCPConfig{Service: service}))
	if err != nil {
		t.Fatal(err)
	}
	server.DB = db
	server.LibraryDB = db
	if response := mcpRequest(t, server, "", "192.0.2.10:1234"); response.Code != http.StatusUnauthorized {
		t.Fatalf("remote without token = %d", response.Code)
	}
	response := mcpRequest(t, server, token, "192.0.2.10:1234")
	if response.Code != http.StatusOK {
		t.Fatalf("bearer = %d %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["err"] != nil || payload["result"] == nil {
		t.Fatal(payload)
	}
	if _, err = db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE id=?`, installation.ID); err != nil {
		t.Fatal(err)
	}
	if response = mcpRequest(t, server, token, "192.0.2.10:1234"); response.Code != http.StatusUnauthorized {
		t.Fatal("disabled plugin accepted token")
	}
}

func TestMCPRejectsRemoteByDefaultAndMapsInstanceReader(t *testing.T) {
	_, service, runtime, installation := mcpHTTPFixture(t)
	token := "fedcba9876543210fedcba9876543210"
	if _, err := runtime.SaveConnection(installation.ID, "instance", 0, map[string]any{"access_token": token}, "agent", map[string]any{"user_id": 1}, nil, "", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ResolveBearer(token, true); err == nil {
		t.Fatal("remote connection enabled by default")
	}
	principal, _, err := service.ResolveBearer(token, false)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != 1 || principal.IsAdmin || principal.AllowWrite {
		t.Fatalf("instance identity = %#v", principal)
	}
	expected := &library.AccessScope{}
	calls := 0
	service.AccessResolver = func(_ context.Context, userID int64, admin bool) (*library.AccessScope, error) {
		calls++
		if userID != 1 || admin {
			t.Fatalf("resolver identity = %d %v", userID, admin)
		}
		return expected, nil
	}
	principal, _, err = service.ResolveBearer(token, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || principal.Access != expected {
		t.Fatal("bearer did not use the shared ACL resolver")
	}
}

func TestMCPLocalCookieUsesCurrentReaderConnection(t *testing.T) {
	db, service, runtime, installation := mcpHTTPFixture(t)
	token := "abcdef0123456789abcdef0123456789"
	if _, err := runtime.SaveConnection(installation.ID, "user", 2, map[string]any{"access_token": token}, "agent", map[string]any{}, nil, "", "agent"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(MCPRouteSpecs(MCPConfig{Service: service}))
	if err != nil {
		t.Fatal(err)
	}
	server.DB = db
	server.LibraryDB = db
	server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: 1}, nil }
	if response := mcpRequest(t, server, "", "127.0.0.1:4321"); response.Code != http.StatusUnauthorized {
		t.Fatal("user 1 inherited user 2 connection")
	}
	server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: 2}, nil }
	if response := mcpRequest(t, server, "", "127.0.0.1:4321"); response.Code != http.StatusOK {
		t.Fatalf("own local connection = %d %s", response.Code, response.Body.String())
	}
}

func TestMCPInitializeOverActualHTTP(t *testing.T) {
	db, service, runtime, installation := mcpHTTPFixture(t)
	token := "00112233445566778899aabbccddeeff"
	if _, err := runtime.SaveConnection(installation.ID, "user", 1, map[string]any{"access_token": token}, "agent", map[string]any{}, nil, "", "agent-http"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewServer(MCPRouteSpecs(MCPConfig{Service: service}))
	if err != nil {
		t.Fatal(err)
	}
	handler.DB = db
	handler.LibraryDB = db
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err = json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || payload.Error != nil || payload.Result.ServerInfo.Name != "BookHarbor" {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
}

func TestMCPValidatesHTTPOrigin(t *testing.T) {
	_, service, runtime, installation := mcpHTTPFixture(t)
	token := "abcdef0123456789abcdef0123456789"
	if _, err := runtime.SaveConnection(installation.ID, "user", 1, map[string]any{"access_token": token}, "agent", map[string]any{}, nil, "", "origin"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(MCPRouteSpecs(MCPConfig{Service: service}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		origin string
		want   int
	}{{"https://another.example", 403}, {"null", 403}, {"http://localhost", 200}, {"", 200}} {
		req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		req.RemoteAddr = "127.0.0.1:4321"
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		if out.Code != tc.want {
			t.Fatalf("origin %q: %d %s", tc.origin, out.Code, out.Body.String())
		}
	}
}

func TestMCPMetadataEditUsesBookManagementPermissions(t *testing.T) {
	db, service, runtime, installation := mcpHTTPFixture(t)
	createCalibreTables(t, db)
	insertBook(t, db, 10, "Original")
	bookSeedItem(t, db, 10, "private", 1)
	wireMCPMetadataEditor(service, ServeOptions{}, db, db)
	token := "abcdef0123456789abcdef0123456789"
	if _, err := runtime.SaveConnection(installation.ID, "user", 1, map[string]any{"access_token": token}, "agent", map[string]any{"allow_write": true}, nil, "", "edit-http"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(MCPRouteSpecs(MCPConfig{Service: service}))
	if err != nil {
		t.Fatal(err)
	}
	rpc := func(name string, args map[string]any) map[string]any {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		body, _ := json.Marshal(mcp.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
		req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(string(body)))
		req.RemoteAddr = "127.0.0.1:4321"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		var rsp struct {
			Result struct {
				IsError           bool
				StructuredContent map[string]any
			}
			Err *mcp.RPCError `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &rsp); err != nil || rec.Code != 200 || rsp.Err != nil || rsp.Result.IsError || rsp.Result.StructuredContent == nil {
			t.Fatalf("%s failed: %s (%v)", name, rec.Body.String(), err)
		}
		return rsp.Result.StructuredContent
	}
	patch := map[string]any{"title": "Edited by Agent", "publisher": "New Press"}
	if err = service.EditMetadata(context.Background(), mcp.Principal{UserID: 2}, 10, patch, true); err == nil {
		t.Fatal("unrelated reader may edit")
	}
	patch["book_id"] = 10
	preview := rpc("book_update", patch)
	var title string
	if err = db.QueryRow(`SELECT title FROM books WHERE id=10`).Scan(&title); err != nil || title != "Original" {
		t.Fatalf("prepare mutated book: %q %v", title, err)
	}
	task := rpc("book_update", map[string]any{"phase": "execute", "operation_id": preview["operation_id"], "confirm": true, "idempotency_key": "edit-test-01"})
	for deadline := time.Now().Add(3 * time.Second); ; {
		state := rpc("task_status", map[string]any{"task_id": task["task_id"]})
		if state["status"] == "completed" {
			break
		}
		if state["status"] == "failed" || time.Now().After(deadline) {
			t.Fatalf("edit task: %v", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	book := rpc("book_detail", map[string]any{"book_id": 10})
	if book["title"] != "Edited by Agent" || book["publisher"] != "New Press" {
		t.Fatalf("edit did not persist: %v", book)
	}
	var locked int
	if err = db.QueryRow(`SELECT count(*) FROM metadata_field_locks WHERE book_id=10`).Scan(&locked); err != nil || locked < 2 {
		t.Fatalf("manual locks: %d %v", locked, err)
	}
}

func TestMCPPersonalTokenUsesSessionIdentityAndRotates(t *testing.T) {
	db, service, _, _ := mcpHTTPFixture(t)
	stateHandler := UserPluginStateHandler(PluginConfig{Settings: service.Settings, Registry: service.Registry})
	specs := append(MCPRouteSpecs(MCPConfig{Service: service}), RouteSpec{Pattern: "/api/plugins/([a-z0-9.-]+)", Methods: []string{"GET", "POST"}, Auth: true, Handler: stateHandler})
	server, err := NewServer(specs)
	if err != nil {
		t.Fatal(err)
	}
	server.DB = db
	userID := int64(1)
	server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: userID}, nil }
	get := func(method, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost/api/plugins/talebook.combo.mcp?personal_token=1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:4321"
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		var response map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || response["err"] != "ok" || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("personal token response: %d %v", out.Code, response["err"])
		}
		return response
	}
	first := get("POST", `{"action":"reveal","user_id":2,"access_token":"ignored"}`)
	token := first["access_token"].(string)
	if first["username"] != "one" || first["allow_write"] != false || len(token) != 64 {
		t.Fatal("wrong default identity or token")
	}
	if status := get("GET", ""); status["access_token"] != nil || status["configured"] != true {
		t.Fatal("status must omit credentials")
	}
	principal, _, err := service.ResolveBearer(token, true)
	if err != nil || principal.UserID != 1 || principal.AllowWrite {
		t.Fatal("token has wrong identity or permission", err)
	}
	userID = 2
	second := get("POST", `{"action":"reveal"}`)
	if second["access_token"] == token || second["username"] != "two" {
		t.Fatal("account collision")
	}
	userID = 1
	if repeated := get("POST", `{"action":"reveal"}`); repeated["access_token"] != token {
		t.Fatal("reveal rotated token")
	}
	get("POST", `{"action":"settings","allow_write":true}`)
	principal, _, err = service.ResolveBearer(token, true)
	if err != nil || !principal.AllowWrite {
		t.Fatal("write setting not applied", err)
	}
	newer := get("POST", `{"action":"rotate"}`)["access_token"].(string)
	if _, _, err = service.ResolveBearer(token, true); err == nil {
		t.Fatal("old token still authorized")
	}
	principal, _, err = service.ResolveBearer(newer, true)
	if err != nil || principal.UserID != 1 || !principal.AllowWrite {
		t.Fatal("rotation did not retain settings", err)
	}
	if p, _, err := service.ResolveBearer(second["access_token"].(string), true); err != nil || p.UserID != 2 {
		t.Fatal("rotation affected another account", err)
	}
}

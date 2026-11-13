package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

func TestAccessCapabilitiesRejectLegacyAndForwardedProcesses(t *testing.T) {
	db := netSchemaDB(t)
	if err := library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if err := userdb.MigrateAccessVisibility(db); err != nil {
		t.Fatal(err)
	}
	r := &accessRuntime{store: library.AccessStore{DB: db}, users: db}
	local, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	local.AccessCapability = r.capabilityProbe("secret")
	dispatcher, err := NewDispatcher(local, "http://127.0.0.1:1", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(dispatcher)
	defer server.Close()
	if err := r.checkCapabilities("secret", server.URL)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.checkCapabilities("wrong-secret", server.URL)(context.Background()); err == nil {
		t.Fatal("wrong runtime accepted")
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"err":"ok"}`)) }))
	defer legacy.Close()
	if err := r.checkCapabilities("secret", legacy.URL)(context.Background()); err == nil {
		t.Fatal("legacy accepted")
	}
	if err := r.checkCapabilities("secret", "http://192.0.2.1:8000")(context.Background()); err == nil {
		t.Fatal("nonloopback accepted")
	}
	request := httptest.NewRequest("GET", accessCapabilityPath, nil)
	request.RemoteAddr = "127.0.0.1:1000"
	request.Header.Set("X-BookHarbor-Access-Challenge", strings.Repeat("ab", 32))
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	r.capabilityProbe("secret")(w, request)
	if w.Code != 404 {
		t.Fatal("forwarding bypass", w.Code)
	}
}

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"talebook/booksvr/internal/metabase"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginruntime"
)

func TestServeDeviceTypesFollowEnabledPlugins(t *testing.T) {
	db := netSchemaDB(t)
	createCalibreTables(t, db)
	user := seedReader(t, db, "device-owner", "password123", true, true)
	registry := pluginruntime.NewPluginRegistry()
	if err := pluginbuiltin.Register(registry); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newServeDispatcher(ServeOptions{PluginRegistry: registry, Settings: map[string]any{}, ServeAll: true}, db, db)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: user, IsAdmin: true}, nil }
	server := httptest.NewServer(dispatcher)
	defer server.Close()
	check := func(want int, boox bool) {
		t.Helper()
		status, result := getJSON(t, server.URL+"/api/user/devices")
		if status != 200 || result["err"] != "ok" {
			t.Fatalf("status=%d result=%v", status, result)
		}
		types, _ := result["device_types"].([]any)
		if len(types) != want {
			t.Fatalf("types=%v want=%d", types, want)
		}
		found := false
		for _, raw := range types {
			item := raw.(map[string]any)
			if item["value"] == "boox" {
				found = true
			}
		}
		if found != boox {
			t.Fatalf("BOOX present=%v want=%v", found, boox)
		}
	}
	check(7, true)
	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	check(7, true)
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("reading existing device types rewrote database: %d -> %d", before, after)
	}
	if _, err := db.Exec(`UPDATE plugin_installations SET enabled=0 WHERE plugin_key='talebook.push.boox'`); err != nil {
		t.Fatal(err)
	}
	check(6, false)
	if _, err := db.Exec(`UPDATE plugin_installations SET enabled=1 WHERE plugin_key='talebook.push.boox'`); err != nil {
		t.Fatal(err)
	}
	check(7, true)
}

func (p *metadataHTTPProvider) GetCover(string, map[string]any) (*pluginiface.CoverResult, error) {
	return nil, nil
}

type metadataHTTPProvider struct{ *metabase.MetaSourceMixin }

func (p *metadataHTTPProvider) Manifest() map[string]any {
	return metabase.MetaManifest("bookharbor.meta.google-books", "Google Books", "test", "mdi-book", "https://books.google.com/", metabase.ManifestOptions{})
}
func (p *metadataHTTPProvider) Search(_ plugindomain.MetadataQuery, _ map[string]any) ([]map[string]any, error) {
	return []map[string]any{{"provider_value": "volume1", "title": "Pride and Prejudice", "publisher": "Penguin", "comments": "A novel"}}, nil
}
func (p *metadataHTTPProvider) Fetch(_ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}
func (p *metadataHTTPProvider) SearchBooks(q plugindomain.MetadataQuery, c map[string]any) ([]plugindomain.BookMetadata, error) {
	return p.MetaSourceMixin.SearchBooks(q, c)
}
func (p *metadataHTTPProvider) Invoke(m string, a []any, c map[string]any) (any, error) {
	return pluginbuiltin.DispatchInvoke(p, m, a, c)
}

func TestServeMetadataSearchPreservesTypedCandidates(t *testing.T) {
	db := netSchemaDB(t)
	createCalibreTables(t, db)
	user := seedReader(t, db, "metadata-owner", "password123", true, true)
	registry := pluginruntime.NewPluginRegistry()
	provider := &metadataHTTPProvider{}
	provider.MetaSourceMixin = metabase.NewMetaSourceMixin(provider)
	if _, err := registry.Register(provider, pluginiface.CapabilitySet{Interfaces: map[string]bool{"MetadataProvider": true}, HasSelfCheck: true}); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newServeDispatcher(ServeOptions{PluginRegistry: registry, Settings: map[string]any{}, ServeAll: true}, db, db)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.server.Identify = func(*http.Request) (Identity, error) { return Identity{UserID: user, IsAdmin: true}, nil }
	server := httptest.NewServer(dispatcher)
	defer server.Close()
	status, result := postJSON(t, server.URL+"/api/admin/plugins/bookharbor.meta.google-books/metadata/search", map[string]any{"query": map[string]any{"isbn": "9780141439518", "title": "Pride and Prejudice"}})
	if status != 200 || result["err"] != "ok" {
		t.Fatalf("status=%d result=%v", status, result)
	}
	items, _ := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("typed metadata was lost in HTTP response: %v", result)
	}
	book := items[0].(map[string]any)
	if book["title"] != "Pride and Prejudice" || book["publisher"] != "Penguin" || book["comments"] != "A novel" {
		t.Fatalf("candidate=%v", book)
	}
}

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"talebook/booksvr/internal/plugindomain"
)

func TestNetworkDispatcherUsesRegisteredProviders(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		name := "normal"
		if maintenance {
			name = "maintenance"
		}
		t.Run(name, func(t *testing.T) {
			fx := newNetFixture(t, "talebook.test.wired-source", func(method string, args []any, ctx map[string]any) (any, error) {
				switch method {
				case "get_categories":
					return []plugindomain.Category{{ID: "fiction", Name: "小说"}}, nil
				case "search", "browse":
					return &plugindomain.Page{Items: []any{map[string]any{"title": "三体", "book_url": "https://example.org/book"}}}, nil
				case "get_book":
					return &plugindomain.SourceBookDetail{ExternalID: "fixture", Title: "三体", TOCRef: "toc"}, nil
				case "get_chapter":
					return &plugindomain.SourceContent{Title: "第一章", Content: "正文"}, nil
				}
				return nil, nil
			})
			createCalibreTables(t, fx.DB)
			user := seedReader(t, fx.DB, "network-owner", "password123", true, true)
			opts := ServeOptions{Addr: "127.0.0.1:8004", MetadataOnly: maintenance, ServeAll: true, Settings: fx.Cfg.Settings, PluginRegistry: fx.Catalog.Runtime.Registry, CookieSecret: "fixture-secret"}
			d, err := newServeDispatcher(opts, fx.DB, fx.DB)
			if err != nil {
				t.Fatal(err)
			}
			guest := false
			d.server.Identify = func(*http.Request) (Identity, error) {
				if guest {
					return Identity{}, nil
				}
				return Identity{UserID: user, IsAdmin: true}, nil
			}
			request := func(path string) map[string]any {
				t.Helper()
				r := httptest.NewRecorder()
				d.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
				var got map[string]any
				if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
					t.Fatal(path, r.Code, r.Body.String())
				}
				return got
			}
			for _, path := range []string{"/api/book-sources", "/api/network/sources"} {
				got := request(path)
				if got["err"] != "ok" || len(got["items"].([]any)) != 1 {
					t.Fatal(path, got)
				}
			}
			source := request("/api/book-sources")["items"].([]any)[0].(map[string]any)["source_key"].(string)
			query := "?source_id=" + url.QueryEscape(source)
			for _, path := range []string{"/api/book-sources/categories" + query, "/api/book-sources/browse" + query + "&url=fiction", "/api/book-sources/book" + query + "&book_url=https://example.org/book", "/api/book-sources/content" + query + "&chapter_url=https://example.org/chapter"} {
				got := request(path)
				if got["err"] != "ok" {
					t.Fatal(path, got)
				}
			}
			search := request("/api/book-sources/search?key=fixture&mode=all")
			if search["err"] != "ok" || search["total"] != float64(1) {
				t.Fatal(search)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				got := request("/api/book-sources/search/status?task_id=" + search["task_id"].(string))
				if got["err"] != "ok" {
					t.Fatal(got)
				}
				if got["finished"] == true {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("search did not complete", got)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := fx.DB.Exec("UPDATE plugin_installations SET enabled=0"); err != nil {
				t.Fatal(err)
			}
			got := request("/api/book-sources")
			if got["err"] != "ok" || len(got["items"].([]any)) != 0 {
				t.Fatal("disabled source leaked", got)
			}
			guest = true
			for _, path := range []string{"/api/book-sources", "/api/network/sources", "/api/book-sources/comic/pages" + query} {
				if got := request(path); got["err"] != "user.need_login" {
					t.Fatal("guest allowed", path, got)
				}
			}
		})
	}
}

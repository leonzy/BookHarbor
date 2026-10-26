package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeReaderFormatResources(t *testing.T) {
	for _, format := range []string{"mobi", "azw", "azw3"} {
		t.Run(format, func(t *testing.T) {
			w, path := epubResourceWiring(t)
			if _, err := w.libraryDB.Exec(`UPDATE book_formats SET format=? WHERE book_id=1`, strings.ToUpper(format)); err != nil {
				t.Fatal(err)
			}
			rec := serveBootstrap(t, w, "engine=readest&format="+format)
			if rec.Code != 200 {
				t.Fatalf("bootstrap %d %s", rec.Code, rec.Body.String())
			}
			var payload struct {
				Book       struct{ Format string }
				Resource   struct{ URL, Mime string }
				Navigation struct{ Fallback string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Book.Format != format || payload.Resource.Mime != readerFormatMIME[format] || payload.Navigation.Fallback != "/book/1?convert=epub" {
				t.Fatalf("unexpected contract: %+v", payload)
			}
			srv, err := NewServer(BookRouteSpecs(w.Config("")))
			if err != nil {
				t.Fatal(err)
			}
			srv.DB = w.db
			serveReadAsAdmin(t, srv, 1)
			for _, method := range []string{http.MethodHead, http.MethodGet} {
				request := httptest.NewRequest(method, payload.Resource.URL, nil)
				if method == http.MethodGet {
					request.Header.Set("Range", "bytes=0-15")
				}
				output := httptest.NewRecorder()
				srv.ServeHTTP(output, request)
				want := 200
				if method == http.MethodGet {
					want = 206
				}
				if output.Code != want || output.Header().Get("Content-Type") != payload.Resource.Mime || output.Header().Get("Accept-Ranges") != "bytes" || output.Header().Get("ETag") == "" {
					t.Fatalf("%s: %d %v", method, output.Code, output.Header())
				}
				if method == http.MethodHead && output.Body.Len() != 0 {
					t.Fatal("HEAD had a body")
				}
				if method == http.MethodGet && output.Body.Len() != 16 {
					t.Fatalf("range length %d", output.Body.Len())
				}
			}
			stale := httptest.NewRecorder()
			srv.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/read/resource/1."+format+"?revision=stale", nil))
			if stale.Code != http.StatusPreconditionFailed {
				t.Fatalf("stale resource=%d", stale.Code)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			deniedCfg := w.Config("")
			deniedCfg.CanViewBook = func(*Context, int64) bool { return false }
			denied, _ := NewServer(BookRouteSpecs(deniedCfg))
			denied.DB = w.db
			denied.Identify = srv.Identify
			forbidden := httptest.NewRecorder()
			denied.ServeHTTP(forbidden, httptest.NewRequest(http.MethodGet, payload.Resource.URL, nil))
			if forbidden.Code == 200 || forbidden.Code == 206 {
				t.Fatal("private source bypassed authorization")
			}
			if got := serveBootstrap(t, w, "engine=readest&format=pdf"); got.Code != 404 {
				t.Fatalf("unsupported requested format %d", got.Code)
			}
		})
	}
}

func TestReaderOnlyExistingDatabaseDoesNotMigrateOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE sentinel(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reader, err := openExistingReaderDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err = reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("schema mutated: %d %v", count, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if d, e := openExistingReaderDB(missing); e == nil {
		d.Close()
		t.Fatal("created a missing database")
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("missing DB was created")
	}
	// Construction cannot start schema migrations or service/background factories.
	opts := ServeOptions{ReaderOnly: true, Addr: "127.0.0.1:0", CookieSecret: "test", TornadoURL: "http://127.0.0.1:1"}
	if _, err = newReaderOnlyDispatcher(opts, reader, reader); err != nil {
		t.Fatal(err)
	}
	if err = reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("reader construction migrated schema")
	}
	opts.Addr = "0.0.0.0:8002"
	if _, err = newReaderOnlyDispatcher(opts, reader, reader); err == nil {
		t.Fatal("public reader-only listener allowed")
	}
	opts.Addr = "127.0.0.1:0"
	opts.DBPath = path
	called := false
	opts.ServiceFactory = func(*sql.DB, *sql.DB) ServiceSet { called = true; return ServiceSet{} }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = Serve(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("reader-only invoked background service factory")
	}
}

func TestNativeReaderExplicitFormatPreservesExistingEPUBPriority(t *testing.T) {
	w, source := epubResourceWiring(t)
	if _, err := w.libraryDB.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,'AZW3',?,100,0)`, source); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ query, format string }{
		{"engine=readest", "epub"},
		{"engine=readest&format=azw3", "azw3"},
	} {
		result := serveBootstrap(t, w, check.query)
		var payload struct{ Book struct{ Format string } }
		if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil || payload.Book.Format != check.format {
			t.Fatalf("%s: %d %s", check.query, result.Code, result.Body.String())
		}
	}
	srv, err := NewServer(BookRouteSpecs(w.Config("")))
	if err != nil {
		t.Fatal(err)
	}
	srv.DB = w.db
	serveReadAsAdmin(t, srv, 1)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/read/1?format=azw3", nil))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "format=azw3") {
		t.Fatalf("explicit native format not honored: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

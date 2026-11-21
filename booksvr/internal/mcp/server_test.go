package mcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/mcp"
)

func fixture(t *testing.T) (*sql.DB, *mcp.Service, mcp.Principal) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "library.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	statements := []string{
		`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT NOT NULL DEFAULT '',author_sort TEXT NOT NULL DEFAULT '',path TEXT NOT NULL DEFAULT '',timestamp INTEGER NOT NULL DEFAULT 0,pubdate TEXT,series_index REAL NOT NULL DEFAULT 1,sort TEXT,has_cover INTEGER NOT NULL DEFAULT 0,uuid TEXT,last_modified TEXT)`,
		`CREATE TABLE authors(id INTEGER PRIMARY KEY,name TEXT,sort TEXT,link TEXT)`, `CREATE TABLE books_authors_link(id INTEGER PRIMARY KEY,book INTEGER,author INTEGER)`,
		`CREATE TABLE tags(id INTEGER PRIMARY KEY,name TEXT,link TEXT)`, `CREATE TABLE books_tags_link(id INTEGER PRIMARY KEY,book INTEGER,tag INTEGER)`,
		`CREATE TABLE publishers(id INTEGER PRIMARY KEY,name TEXT,sort TEXT,link TEXT)`, `CREATE TABLE books_publishers_link(id INTEGER PRIMARY KEY,book INTEGER,publisher INTEGER)`,
		`CREATE TABLE series(id INTEGER PRIMARY KEY,name TEXT,sort TEXT,link TEXT)`, `CREATE TABLE books_series_link(id INTEGER PRIMARY KEY,book INTEGER,series INTEGER)`,
		`CREATE TABLE ratings(id INTEGER PRIMARY KEY,rating INTEGER,link TEXT)`, `CREATE TABLE books_ratings_link(id INTEGER PRIMARY KEY,book INTEGER,rating INTEGER)`,
		`CREATE TABLE languages(id INTEGER PRIMARY KEY,lang_code TEXT,link TEXT)`, `CREATE TABLE books_languages_link(id INTEGER PRIMARY KEY,book INTEGER,lang_code INTEGER,item_order INTEGER)`,
		`CREATE TABLE comments(id INTEGER PRIMARY KEY,book INTEGER,text TEXT)`, `CREATE TABLE identifiers(id INTEGER PRIMARY KEY,book INTEGER,type TEXT,val TEXT)`,
		`CREATE TABLE data(id INTEGER PRIMARY KEY,book INTEGER,format TEXT,uncompressed_size INTEGER,name TEXT)`,
		`CREATE TABLE custom_columns(id INTEGER PRIMARY KEY,label TEXT,name TEXT,datatype TEXT,display TEXT,is_multiple INTEGER,normalized INTEGER)`,
	}
	for _, statement := range statements {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = library.Migrate(db); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../userdb/testdata/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO readers(id,username,admin,active,permission) VALUES(1,'one',0,1,'rps'),(2,'two',0,1,'rps')`,
		`INSERT INTO books(id,title,author_sort,path,timestamp) VALUES(1,'三体','刘慈欣','/private/one',100),(2,'秘密书','别人','/private/two',101),(3,'活着','余华','/private/three',102)`,
		`INSERT INTO authors(id,name) VALUES(1,'刘慈欣'),(2,'别人'),(3,'余华')`, `INSERT INTO books_authors_link(book,author) VALUES(1,1),(2,2),(3,3)`,
		`INSERT INTO tags(id,name) VALUES(1,'科幻'),(2,'私人'),(3,'中国文学')`, `INSERT INTO books_tags_link(book,tag) VALUES(1,1),(2,2),(3,3)`,
		`INSERT INTO ratings(id,rating) VALUES(1,8),(2,9)`, `INSERT INTO books_ratings_link(book,rating) VALUES(1,1),(3,2)`,
		`INSERT INTO data(book,format,uncompressed_size,name) VALUES(1,'EPUB',100,'one'),(2,'EPUB',100,'two'),(3,'TXT',100,'three')`,
		`INSERT INTO items(book_id,count_guest,count_visit,count_download,website,collector_id,scope,book_type,media_type,media_type_locked,src_path) VALUES(1,0,0,0,'',1,'public','','',0,''),(2,0,0,0,'',2,'private','','',0,''),(3,0,0,0,'',1,'public','','',0,'')`,
		`INSERT INTO reading_state(book_id,reader_id,read_state,read_date,progress) VALUES(1,1,1,'2026-01-01','{"chapter":3}'),(2,2,2,'2026-01-02','{}')`,
	}
	for _, statement := range seed {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	service := mcp.NewService(db, db, map[string]any{}, nil)
	principal := mcp.Principal{UserID: 1, Local: true, Scopes: []string{"books.read", "reading.read", "tasks.read"}}
	return db, service, principal
}

func call(t *testing.T, service *mcp.Service, principal mcp.Principal, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	response := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: raw})
	if response == nil || response.Error != nil {
		t.Fatalf("RPC error: %#v", response)
	}
	result := response.Result.(map[string]any)
	if result["isError"] == true {
		t.Fatalf("tool error: %v", result)
	}
	return result["structuredContent"].(map[string]any)
}

func TestProtocolLifecycleAndToolVisibility(t *testing.T) {
	_, service, principal := fixture(t)
	response := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`"init"`), Method: "initialize", Params: json.RawMessage(`{}`)})
	if response.Result.(map[string]any)["protocolVersion"] != mcp.ProtocolVersion {
		t.Fatal(response)
	}
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"})
	tools := response.Result.(map[string]any)["tools"].([]map[string]any)
	for _, tool := range tools {
		schema, _ := json.Marshal(tool["inputSchema"])
		if strings.Contains(string(schema), `"required":null`) {
			t.Fatal(string(schema))
		}
		if tool["name"] == "book_send" || tool["name"] == "book_import" || tool["name"] == "book_update" {
			t.Fatal("read-only connection exposed writes")
		}
	}
	if notification := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", Method: "notifications/initialized"}); notification != nil {
		t.Fatal("notification must not respond")
	}
}

func TestSearchHistoryAndRecommendationRespectOwnership(t *testing.T) {
	_, service, principal := fixture(t)
	found := call(t, service, principal, "book_search", map[string]any{"query": "title:三体 OR title:秘密书"})
	books := found["books"].([]map[string]any)
	if len(books) != 1 || books[0]["book_id"] != int64(1) {
		t.Fatalf("private book leaked: %v", books)
	}
	encoded, _ := json.Marshal(found)
	if strings.Contains(string(encoded), "/private/") {
		t.Fatalf("path leaked: %s", encoded)
	}
	history := call(t, service, principal, "reading_history", map[string]any{"state": "all"})
	entries := history["history"].([]map[string]any)
	if len(entries) != 1 || entries[0]["book"].(map[string]any)["book_id"] != int64(1) {
		t.Fatalf("history isolation: %v", entries)
	}
	recommended := call(t, service, principal, "book_recommend", map[string]any{"request": "推荐中国文学，不要我读过的", "limit": 5})
	if recommended["personalized"] != true {
		t.Fatal("reading seeds were not included in the preference profile")
	}
	items := recommended["recommendations"].([]map[string]any)
	if len(items) == 0 {
		t.Fatal("expected recommendation")
	}
	for _, item := range items {
		id := item["book"].(map[string]any)["book_id"]
		if id == int64(1) || id == int64(2) {
			t.Fatalf("read/private recommendation: %v", items)
		}
	}
	other := principal
	other.UserID = 2
	otherHistory := call(t, service, other, "reading_history", map[string]any{})
	otherEntries := otherHistory["history"].([]map[string]any)
	if len(otherEntries) != 1 || otherEntries[0]["book"].(map[string]any)["book_id"] != int64(2) {
		t.Fatal("user must see only their own history and private book")
	}
}

func TestPaginationScopesAndInvalidRequests(t *testing.T) {
	_, service, principal := fixture(t)
	response := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"reading_history","arguments":{"limit":0}}`)})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("invalid limit accepted")
	}
	principal.Scopes = []string{"books.read"}
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"reading_history","arguments":{}}`)})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("missing reading scope accepted")
	}
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"})
	for _, tool := range response.Result.(map[string]any)["tools"].([]map[string]any) {
		if tool["name"] == "reading_history" || tool["name"] == "task_status" {
			t.Fatal("tool list exceeded scopes")
		}
	}
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "1.0", ID: json.RawMessage(`1`), Method: "ping"})
	if response.Error == nil || response.Error.Code != -32600 {
		t.Fatal("invalid JSON-RPC accepted")
	}
}

func TestWriteDisabledAndUnknownTaskAreSafe(t *testing.T) {
	_, service, principal := fixture(t)
	response := service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"book_send","arguments":{"book_id":1,"device_type":"kindle","target":"a@example.com"}}`)})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("write accepted without scope")
	}
	principal.Scopes = append(principal.Scopes, "books.write")
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"book_send","arguments":{"book_id":1,"device_type":"kindle","target":"a@example.com"}}`)})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("write accepted while disabled")
	}
	principal.AllowWrite = true
	response = service.Handle(context.Background(), principal, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"task_status","arguments":{"task_id":"mcp_other"}}`)})
	if response.Result.(map[string]any)["isError"] != true {
		t.Fatal("unknown task accepted")
	}
}

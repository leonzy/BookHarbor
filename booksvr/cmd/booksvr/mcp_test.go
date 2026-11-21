package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talebook/booksvr/internal/httpapi"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/mcp"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

func TestMCPRequiresExplicitReaderAndDatabase(t *testing.T) {
	var output bytes.Buffer
	err := runMCP(nil, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "user-id") {
		t.Fatalf("explicit reader required: %v", err)
	}
}

func TestUsageIncludesMCP(t *testing.T) {
	if !strings.Contains(usage, "booksvr mcp") && !strings.Contains(usage, "  mcp ") {
		t.Fatal("MCP missing from usage")
	}
}

func TestMCPStdioJSONRPCEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT NOT NULL DEFAULT '',author_sort TEXT NOT NULL DEFAULT '',path TEXT NOT NULL DEFAULT '',timestamp INTEGER NOT NULL DEFAULT 0,pubdate TEXT,series_index REAL NOT NULL DEFAULT 1,sort TEXT,has_cover INTEGER NOT NULL DEFAULT 0,uuid TEXT,last_modified TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = library.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err = library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../internal/userdb/testdata/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO readers(id,username,admin,active,permission,create_time) VALUES(1,'reader',0,1,'rps','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err = userdb.MigrateAccessVisibility(db); err != nil {
		t.Fatal(err)
	}
	registry := pluginruntime.NewPluginRegistry()
	if err = pluginbuiltin.Register(registry); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"PLUGIN_SECRET_KEY": "stdio-test-key"}
	if _, err = pluginruntime.EnsureBuiltinInstallations(db, 1, settings, registry); err != nil {
		t.Fatal(err)
	}
	installation, err := userdb.GetPluginInstallationByKey(db, mcp.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE plugin_installations SET enabled=1,status='active' WHERE id=?`, installation.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	settingsFile := filepath.Join(filepath.Dir(path), "auto.py")
	if err = httpapi.SaveSettings(settingsFile, map[string]any{"PLUGIN_SECRET_KEY": "stdio-test-key", "cookie_secret": "fixture"}); err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	var output bytes.Buffer
	if err = runMCP([]string{"-db=" + path, "-user=reader", "-cookie-secret=fixture", "-settings-file=" + settingsFile}, input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses=%d output=%s", len(lines), output.String())
	}
	for index, line := range lines {
		var response map[string]any
		if err = json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		if response["result"] == nil || response["error"] != nil {
			t.Fatalf("response %d: %v", index, response)
		}
	}
}

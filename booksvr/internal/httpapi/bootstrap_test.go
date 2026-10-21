package httpapi

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestBootstrapCreatesNativeSchemaAndPreservesSettings(t *testing.T) {
	root := t.TempDir()
	dbpath := filepath.Join(root, "users.db")
	settings := filepath.Join(root, "settings", "auto.py")
	if err := Bootstrap(dbpath, settings); err != nil {
		t.Fatal(err)
	}
	secret := toStrSetting(LoadSettings(settings), "cookie_secret")
	if len(secret) != 64 {
		t.Fatal("missing generated secret")
	}
	db, err := sql.Open("sqlite", dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range userTableNames() {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(table, "sqlite")).Scan(&n); err != nil {
			t.Fatal(table, err)
		}
	}
	if err := Bootstrap(dbpath, settings); err != nil {
		t.Fatal(err)
	}
	if toStrSetting(LoadSettings(settings), "cookie_secret") != secret {
		t.Fatal("secret replaced")
	}
}

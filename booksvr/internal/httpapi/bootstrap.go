package httpapi

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Bootstrap owns first installation and additive user schema upgrades. Existing
// accounts, settings, reading states and import intents are never replaced.
func Bootstrap(userPath, settingsPath string) error {
	if err := os.MkdirAll(filepath.Dir(userPath), 0755); err != nil {
		return err
	}
	db, err := openUserDB(userPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureUserSchema(db); err != nil {
		return err
	}
	settings := LoadSettings(settingsPath)
	secret := toStrSetting(settings, "cookie_secret")
	if secret == "" || secret == "cookie_secret" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		settings["cookie_secret"] = hex.EncodeToString(b)
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
			return err
		}
		if err := SaveSettings(settingsPath, settings); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS items_recommendation_heat ON items((COALESCE(count_visit,0)+2*COALESCE(count_download,0)) DESC,book_id DESC)`)
	return err
}

func ensureUserSchema(db *sql.DB) error {
	if err := createUserTables(db, "sqlite"); err != nil {
		return err
	}
	// Columns only: constraints and destructive transformations are not inferred.
	column := regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z_0-9]*)\s+((?:VAR)?CHAR\(\d+\)|INTEGER|BIGINT|TEXT|FLOAT|BOOLEAN|DATETIME|DATE|JSON)(?:\s|,|$)`)
	for _, table := range createTableRe.FindAllStringSubmatch(userSchemaSQL, -1) {
		rows, err := db.Query(`PRAGMA table_info("` + table[1] + `")`)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			existing[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range column.FindAllStringSubmatch(table[2], -1) {
			if !existing[c[1]] {
				if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, quoteIdent(table[1], "sqlite"), quoteIdent(c[1], "sqlite"), c[2])); err != nil {
					return err
				}
			}
		}
	}
	// Embed the canonical schema's explicit indexes, including unique reading keys.
	for _, stmt := range strings.Split(userSchemaSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if strings.HasPrefix(stmt, "CREATE INDEX ") || strings.HasPrefix(stmt, "CREATE UNIQUE INDEX ") {
			stmt = strings.Replace(stmt, "INDEX ", "INDEX IF NOT EXISTS ", 1)
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

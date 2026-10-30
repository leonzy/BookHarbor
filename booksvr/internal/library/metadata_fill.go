package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/localmeta"
)

// EnsureMetadataSchema is additive and independent of expensive library migrations.
func EnsureMetadataSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS metadata_enrichment_writes(id INTEGER PRIMARY KEY,book_id INTEGER NOT NULL,field TEXT NOT NULL,origin TEXT NOT NULL,previous_value TEXT NOT NULL,written_value TEXT NOT NULL,created_at INTEGER NOT NULL DEFAULT(unixepoch()))`,
		`CREATE INDEX IF NOT EXISTS metadata_enrichment_writes_book ON metadata_enrichment_writes(book_id,id)`,
		`CREATE TABLE IF NOT EXISTS metadata_field_locks(book_id INTEGER NOT NULL,field TEXT NOT NULL,PRIMARY KEY(book_id,field))`,
		`CREATE TABLE IF NOT EXISTS metadata_enrichment_state(key TEXT PRIMARY KEY,value INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS metadata_enrichment_jobs(book_id INTEGER PRIMARY KEY,status TEXT NOT NULL,next_at INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS metadata_enrichment_due ON metadata_enrichment_jobs(status,next_at,book_id)`,
		`CREATE TABLE IF NOT EXISTS metadata_enrichment_lease(id INTEGER PRIMARY KEY,owner TEXT NOT NULL,expires INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// LockMetadata protects explicit user choices, including intentional empty values.
// It belongs in the same transaction as the corresponding manual edit.
func LockMetadata(tx *sql.Tx, id int64, fields []string) error {
	for _, f := range fields {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO metadata_field_locks(book_id,field) VALUES(?,?)`, id, f); err != nil {
			return err
		}
	}
	return nil
}

// Older versions did not record field provenance. If a historical book was
// modified after import, conservatively protect even its empty fields.
func ProtectHistoricalMetadataEdits(db *sql.DB, id int64) error {
	var modified, created int64
	err := db.QueryRow(`SELECT coalesce(CAST(strftime('%s',last_modified) AS INTEGER),0),CASE WHEN typeof(timestamp) IN ('integer','real') THEN CAST(timestamp AS INTEGER) ELSE coalesce(CAST(strftime('%s',timestamp) AS INTEGER),0) END FROM books WHERE id=?`, id).Scan(&modified, &created)
	if err != nil {
		return err
	}
	if created <= 0 || modified <= created {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = LockMetadata(tx, id, []string{"title", "authors", "author_sort", "publisher", "isbn", "language", "pubdate", "comments", "tags"}); err != nil {
		return err
	}
	return tx.Commit()
}

// FillMissingMetadata rechecks values after obtaining the SQLite write lock. No
// extraction, network operation or sleep runs inside this short transaction.
func FillMissingMetadata(db *sql.DB, id int64, m importmeta.Metadata, origin ...string) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE books SET id=id WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := tx.Query(`SELECT field FROM metadata_field_locks WHERE book_id=?`, id)
	if err != nil {
		return nil, err
	}
	locks := map[string]bool{}
	for rows.Next() {
		var f string
		if err = rows.Scan(&f); err != nil {
			rows.Close()
			return nil, err
		}
		locks[f] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var before map[string]string
	if len(origin) > 0 {
		before, err = metadataSnapshot(tx, id)
		if err != nil {
			return nil, err
		}
	}
	changed := []string{}
	if !locks["authors"] && !localmeta.MissingAuthors(m.Authors) {
		r, e := tx.Query(`SELECT a.name FROM authors a JOIN books_authors_link l ON l.author=a.id WHERE l.book=?`, id)
		if e != nil {
			return nil, e
		}
		var current []string
		for r.Next() {
			var a string
			if e = r.Scan(&a); e != nil {
				r.Close()
				return nil, e
			}
			current = append(current, a)
		}
		e = r.Err()
		r.Close()
		if e != nil {
			return nil, e
		}
		if localmeta.MissingAuthors(current) {
			if _, err = tx.Exec(`DELETE FROM books_authors_link WHERE book=?`, id); err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, a := range m.Authors {
				a = strings.TrimSpace(a)
				if localmeta.UnknownAuthor(a) || seen[a] {
					continue
				}
				seen[a] = true
				aid, e := findOrCreateItem(tx, "authors", "name", a)
				if e != nil {
					return nil, e
				}
				if _, e = tx.Exec(`INSERT INTO books_authors_link(book,author) VALUES(?,?)`, id, aid); e != nil {
					return nil, e
				}
			}
			if !locks["author_sort"] {
				if _, err = tx.Exec(`UPDATE books SET author_sort=? WHERE id=?`, strings.Join(m.Authors, " & "), id); err != nil {
					return nil, err
				}
			}
			changed = append(changed, "authors")
		}
	}
	for _, f := range []struct{ field, value, table, col, link, linkcol string }{
		{"publisher", m.Publisher, "publishers", "name", "books_publishers_link", "publisher"},
		{"language", m.Language, "languages", "lang_code", "books_languages_link", "lang_code"},
	} {
		if locks[f.field] || strings.TrimSpace(f.value) == "" {
			continue
		}
		var count int
		if err = tx.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s WHERE book=?`, f.link), id).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			continue
		}
		fid, e := findOrCreateItem(tx, f.table, f.col, f.value)
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(fmt.Sprintf(`INSERT INTO %s(book,%s) VALUES(?,?)`, f.link, f.linkcol), id, fid); e != nil {
			return nil, e
		}
		changed = append(changed, f.field)
	}
	if isbn := localmeta.ISBN(m.ISBN); isbn != "" && !locks["isbn"] {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM identifiers WHERE book=? AND type='isbn' AND trim(val)<>''`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			if _, err = tx.Exec(`DELETE FROM identifiers WHERE book=? AND type='isbn'`, id); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(`INSERT INTO identifiers(book,type,val) VALUES(?,'isbn',?)`, id, isbn); err != nil {
				return nil, err
			}
			changed = append(changed, "isbn")
		}
	}
	if m.Comments != "" && !locks["comments"] {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM comments WHERE book=? AND trim(coalesce(text,''))<>''`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			if _, err = tx.Exec(`DELETE FROM comments WHERE book=?`, id); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(`INSERT INTO comments(book,text) VALUES(?,?)`, id, m.Comments); err != nil {
				return nil, err
			}
			changed = append(changed, "comments")
		}
	}
	if m.Pubdate != "" && !locks["pubdate"] {
		r, e := tx.Exec(`UPDATE books SET pubdate=? WHERE id=? AND trim(coalesce(pubdate,''))=''`, m.Pubdate, id)
		if e != nil {
			return nil, e
		}
		n, _ := r.RowsAffected()
		if n > 0 {
			changed = append(changed, "pubdate")
		}
	}
	if len(m.Tags) > 0 && !locks["tags"] {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM books_tags_link WHERE book=?`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			seen := map[string]bool{}
			for _, tag := range m.Tags {
				tag = strings.TrimSpace(tag)
				if tag == "" || seen[tag] {
					continue
				}
				seen[tag] = true
				tid, e := findOrCreateItem(tx, "tags", "name", tag)
				if e != nil {
					return nil, e
				}
				if _, err = tx.Exec(`INSERT INTO books_tags_link(book,tag) VALUES(?,?)`, id, tid); err != nil {
					return nil, err
				}
			}
			changed = append(changed, "tags")
		}
	}
	if len(changed) > 0 {
		if _, err = tx.Exec(`UPDATE books SET last_modified=datetime('now') WHERE id=?`, id); err != nil {
			return nil, err
		}
	}
	if len(origin) > 0 && len(changed) > 0 {
		after, e := metadataSnapshot(tx, id)
		if e != nil {
			return nil, e
		}
		for field, value := range after {
			if before[field] != value {
				if _, e = tx.Exec(`INSERT INTO metadata_enrichment_writes(book_id,field,origin,previous_value,written_value) VALUES(?,?,?,?,?)`, id, field, origin[0], before[field], value); e != nil {
					return nil, e
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

func metadataSnapshot(tx *sql.Tx, id int64) (map[string]string, error) {
	queries := map[string]string{
		"authors":     `SELECT a.name FROM authors a JOIN books_authors_link l ON l.author=a.id WHERE l.book=? ORDER BY l.id`,
		"tags":        `SELECT t.name FROM tags t JOIN books_tags_link l ON l.tag=t.id WHERE l.book=? ORDER BY t.id`,
		"author_sort": `SELECT coalesce(author_sort,'') FROM books WHERE id=?`,
		"pubdate":     `SELECT coalesce(pubdate,'') FROM books WHERE id=?`,
		"publisher":   `SELECT p.name FROM publishers p JOIN books_publishers_link l ON l.publisher=p.id WHERE l.book=?`,
		"language":    `SELECT p.lang_code FROM languages p JOIN books_languages_link l ON l.lang_code=p.id WHERE l.book=?`,
		"isbn":        `SELECT val FROM identifiers WHERE book=? AND type='isbn' ORDER BY id`,
		"comments":    `SELECT coalesce(text,'') FROM comments WHERE book=?`,
	}
	out := map[string]string{}
	for field, query := range queries {
		rows, e := tx.Query(query, id)
		if e != nil {
			return nil, e
		}
		values := []string{}
		for rows.Next() {
			var v string
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return nil, e
			}
			values = append(values, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		var value any = ""
		if field == "authors" || field == "tags" {
			value = values
		} else if len(values) > 0 {
			value = values[0]
		}
		b, _ := json.Marshal(value)
		out[field] = string(b)
	}
	return out, nil
}

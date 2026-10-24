package library

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SizeOrder accepts only the two public size sort values. Unknown values keep
// the endpoint's original order; no request text is interpolated into SQL.
func SizeOrder(value string) string {
	switch value {
	case "size_asc":
		return "asc"
	case "size_desc":
		return "desc"
	}
	return ""
}

func sizeOrderSQL(direction, alias string) string {
	order := "DESC"
	if direction == "asc" {
		order = "ASC"
	}
	return alias + ".known DESC," + alias + ".size " + order + "," + alias + ".book_id DESC"
}

// A compact materialized ranking avoids per-request MAX/GROUP BY over every
// format in a large library. Only the changed book is recomputed by triggers.
func migrateBookSizes(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('books','book_formats','data','source_sync_formats')`).Scan(&n); err != nil {
		return err
	}
	if n != 4 {
		return nil
	}
	legacy, err := columnExists(db, "data", "uncompressed_size")
	if err != nil {
		return err
	}
	value := `COALESCE((SELECT MAX(v) FROM (
 SELECT f.size v FROM book_formats f WHERE f.book_id=b.id AND f.size>0 AND NOT EXISTS(SELECT 1 FROM source_sync_formats s WHERE s.format_id=f.id AND s.missing=1)`
	if legacy {
		value += ` UNION ALL SELECT d.uncompressed_size v FROM data d WHERE d.book=b.id AND d.uncompressed_size>0 AND NOT EXISTS(SELECT 1 FROM book_formats f WHERE f.book_id=d.book AND UPPER(f.format)=UPPER(d.format))`
	}
	value += `)),0)`
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existed int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='book_sort_sizes'`).Scan(&existed); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS book_sort_sizes(book_id INTEGER PRIMARY KEY,size INTEGER NOT NULL DEFAULT 0,known INTEGER NOT NULL DEFAULT 0)`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	if existed == 0 {
		if _, err = tx.Exec(`INSERT INTO book_sort_sizes(book_id,size,known) SELECT b.id,` + value + `,0 FROM books b`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE book_sort_sizes SET known=(size>0)`); err != nil {
			return err
		}
	}
	// Build covering indexes after the initial bulk load, avoiding two rounds
	// of random index updates for every book during the first large-library start.
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS book_size_asc ON book_sort_sizes(known DESC,size ASC,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS book_size_desc ON book_sort_sizes(known DESC,size DESC,book_id DESC)`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	recompute := func(ids string) string {
		return `DELETE FROM book_sort_sizes WHERE book_id IN (` + ids + `) AND NOT EXISTS(SELECT 1 FROM books WHERE id=book_sort_sizes.book_id); INSERT INTO book_sort_sizes(book_id,size,known) SELECT b.id,0,0 FROM books b WHERE b.id IN (` + ids + `) AND NOT EXISTS(SELECT 1 FROM book_sort_sizes z WHERE z.book_id=b.id); UPDATE book_sort_sizes SET size=(SELECT ` + value + ` FROM books b WHERE b.id=book_sort_sizes.book_id) WHERE book_id IN (` + ids + `); UPDATE book_sort_sizes SET known=(size>0) WHERE book_id IN (` + ids + `);`
	}
	for _, table := range []string{"books", "book_formats", "data", "source_sync_formats"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if table == "books" && event == "UPDATE" {
				continue
			}
			row := func(prefix string) string {
				switch table {
				case "books":
					return "SELECT " + prefix + ".id"
				case "data":
					return "SELECT " + prefix + ".book"
				case "book_formats":
					return "SELECT " + prefix + ".book_id"
				default:
					return "SELECT book_id FROM book_formats WHERE id=" + prefix + ".format_id"
				}
			}
			ids := ""
			if event != "DELETE" {
				ids = row("NEW")
			}
			if event != "INSERT" {
				if ids != "" {
					ids += " UNION "
				}
				ids += row("OLD")
			}
			spec := event
			when := ""
			if event == "UPDATE" {
				switch table {
				case "book_formats":
					spec += " OF size,book_id,format"
				case "data":
					spec += " OF book,format"
					if legacy {
						spec += ",uncompressed_size"
					}
				case "source_sync_formats":
					spec += " OF missing,format_id"
					when = " WHEN OLD.missing IS NOT NEW.missing OR OLD.format_id IS NOT NEW.format_id"
				}
			}
			if table == "source_sync_formats" && event == "INSERT" {
				when = " WHEN NEW.missing=1"
			}
			if table == "source_sync_formats" && event == "DELETE" {
				when = " WHEN OLD.missing=1"
			}
			name := "size_order_" + table + "_" + strings.ToLower(event)
			stmt := fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s%s BEGIN %s END", name, spec, table, when, recompute(ids))
			if _, err = tx.Exec(stmt); err != nil {
				return fmt.Errorf("size order trigger: %w", err)
			}
		}
	}
	return tx.Commit()
}

// SizeSortedIDs sorts the complete candidate set before pagination. json_each
// keeps large candidate sets below SQLite's variable limit.
func SizeSortedIDs(ctx context.Context, db *sql.DB, ids []int64, direction string, start, size int) ([]int64, error) {
	if start < 0 {
		start = 0
	}
	rows, err := db.QueryContext(ctx, `SELECT z.book_id FROM book_sort_sizes z WHERE z.book_id IN (SELECT value FROM json_each(?)) ORDER BY `+sizeOrderSQL(direction, "z")+` LIMIT ? OFFSET ?`, jsonIDs(ids), size, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

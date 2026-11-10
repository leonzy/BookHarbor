package library

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

func EnsureImportSizeAudit(db *sql.DB) error {
	if _, e := db.Exec(`CREATE TABLE IF NOT EXISTS import_size_audit_removed(generation TEXT NOT NULL,format_id INTEGER NOT NULL,book_id INTEGER NOT NULL,path TEXT NOT NULL,format TEXT NOT NULL,minimum INTEGER NOT NULL,applied INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(generation,format_id))`); e != nil {
		return e
	}
	if _, e := db.Exec(`CREATE INDEX IF NOT EXISTS import_size_audit_pending ON import_size_audit_removed(applied)`); e != nil {
		return e
	}
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS import_size_audit(id INTEGER PRIMARY KEY CHECK(id=1),generation TEXT NOT NULL,minimum INTEGER NOT NULL,cursor INTEGER NOT NULL DEFAULT 0,max_id INTEGER NOT NULL,total INTEGER NOT NULL,checked INTEGER NOT NULL DEFAULT 0,removed_formats INTEGER NOT NULL DEFAULT 0,removed_books INTEGER NOT NULL DEFAULT 0,unreadable INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'running',error TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL)`)
	return e
}

// PruneImportFormat touches only database rows; no source files or directories are removed.
// File identity and size are checked again after the SQLite transaction begins.
func PruneImportFormat(ctx context.Context, db *sql.DB, generation string, id int64, path, physical string, before os.FileInfo, minimum int64, unreadable bool) (bool, error) {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var active string
	var configured int64
	if e = tx.QueryRow(`SELECT generation,minimum FROM import_size_audit WHERE id=1`).Scan(&active, &configured); e != nil {
		return false, e
	}
	if generation != active || minimum != configured {
		return false, nil
	}
	var bookID int64
	var current, format string
	e = tx.QueryRow(`SELECT book_id,path,format FROM book_formats WHERE id=?`, id).Scan(&bookID, &current, &format)
	if e != nil && e != sql.ErrNoRows {
		return false, e
	}
	removed, bookRemoved := 0, 0
	if e == nil && current == path && !unreadable {
		after, err := os.Stat(physical)
		if err != nil || before == nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			unreadable = true
		} else if after.Size() < minimum {
			var sourceTable int
			if e = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='source_sync_formats'`).Scan(&sourceTable); e != nil {
				return false, e
			}
			if sourceTable != 0 {
				if _, e = tx.Exec(`DELETE FROM source_sync_formats WHERE format_id=?`, id); e != nil {
					return false, e
				}
			}
			if _, e = tx.Exec(`DELETE FROM book_formats WHERE id=?`, id); e != nil {
				return false, e
			}
			if _, e = tx.Exec(`DELETE FROM data WHERE book=? AND UPPER(format)=UPPER(?)`, bookID, format); e != nil {
				return false, e
			}
			if _, e = tx.Exec(`INSERT OR IGNORE INTO import_size_audit_removed(generation,format_id,book_id,path,format,minimum) VALUES(?,?,?,?,?,?)`, generation, id, bookID, path, format, minimum); e != nil {
				return false, e
			}
			removed = 1
			var remaining int
			if e = tx.QueryRow(`SELECT (SELECT count(*) FROM book_formats WHERE book_id=?)+(SELECT count(*) FROM data WHERE book=?)`, bookID, bookID).Scan(&remaining); e != nil {
				return false, e
			}
			if remaining == 0 {
				if e = deleteBookTx(tx, bookID); e != nil {
					return false, e
				}
				bookRemoved = 1
			}
		} else {
			if _, e = tx.Exec(`UPDATE book_formats SET size=?,mtime=? WHERE id=?`, after.Size(), after.ModTime().Unix(), id); e != nil {
				return false, e
			}
		}
	}
	skipped := 0
	if unreadable {
		skipped = 1
	}
	if _, e = tx.Exec(`UPDATE import_size_audit SET cursor=?,checked=checked+1,removed_formats=removed_formats+?,removed_books=removed_books+?,unreadable=unreadable+?,error='',updated_at=? WHERE id=1 AND generation=?`, id, removed, bookRemoved, skipped, time.Now().Unix(), generation); e != nil {
		return false, fmt.Errorf("size audit checkpoint: %w", e)
	}
	return removed > 0, tx.Commit()
}

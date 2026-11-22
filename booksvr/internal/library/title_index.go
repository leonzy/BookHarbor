package library

import (
	"context"
	"database/sql"
	"strings"
)

func migrateTitleIndex(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS book_title_index(id INTEGER PRIMARY KEY CHECK(id=1),cursor INTEGER NOT NULL);
INSERT OR IGNORE INTO book_title_index VALUES(1,0);
CREATE TABLE IF NOT EXISTS book_title_dirty(book_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS book_title_grams(gram TEXT NOT NULL,book_id INTEGER NOT NULL,PRIMARY KEY(gram,book_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS book_title_grams_book ON book_title_grams(book_id);
CREATE TRIGGER IF NOT EXISTS book_title_insert AFTER INSERT ON books
 WHEN NEW.id<=(SELECT cursor FROM book_title_index WHERE id=1)
 BEGIN INSERT OR IGNORE INTO book_title_dirty VALUES(NEW.id); END;
CREATE TRIGGER IF NOT EXISTS book_title_update AFTER UPDATE OF title ON books
 WHEN NEW.title IS NOT OLD.title AND NEW.id<=(SELECT cursor FROM book_title_index WHERE id=1)
 BEGIN INSERT OR IGNORE INTO book_title_dirty VALUES(NEW.id); END;
CREATE TRIGGER IF NOT EXISTS book_title_delete AFTER DELETE ON books
 WHEN OLD.id<=(SELECT cursor FROM book_title_index WHERE id=1)
 BEGIN INSERT OR IGNORE INTO book_title_dirty VALUES(OLD.id); END;
`)
	return err
}

func titleGrams(title string) []string {
	runes := []rune(strings.ToLower(title))
	seen := make(map[string]bool)
	var grams []string
	for i := 1; i < len(runes); i++ {
		gram := string(runes[i-1 : i+1])
		if !seen[gram] {
			seen[gram] = true
			grams = append(grams, gram)
		}
	}
	return grams
}

// IndexTitleBatch shares a short write transaction between the cursor and its
// postings. Edits behind the cursor appear in the same transaction as dirty IDs.
func IndexTitleBatch(ctx context.Context, db *sql.DB) (bool, error) {
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM book_title_dirty) OR
		EXISTS(SELECT 1 FROM books WHERE id>(SELECT cursor FROM book_title_index WHERE id=1))`).Scan(&pending)
	if err != nil || !pending {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE book_title_index SET cursor=cursor WHERE id=1`); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT 1,d.book_id,COALESCE(b.title,'') FROM
		(SELECT book_id FROM book_title_dirty ORDER BY book_id LIMIT 32) d LEFT JOIN books b ON b.id=d.book_id
		UNION ALL SELECT 0,id,COALESCE(title,'') FROM
		(SELECT id,title FROM books WHERE id>(SELECT cursor FROM book_title_index WHERE id=1) ORDER BY id LIMIT 96)`)
	if err != nil {
		return false, err
	}
	type entry struct {
		dirty bool
		id    int64
		title string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.dirty, &e.id, &e.title); err != nil {
			rows.Close()
			return false, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO book_title_grams(gram,book_id) VALUES(?,?)`)
	if err != nil {
		return false, err
	}
	defer insert.Close()
	var cursor int64
	for _, e := range entries {
		if e.dirty {
			if _, err := tx.ExecContext(ctx, `DELETE FROM book_title_grams WHERE book_id=?`, e.id); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM book_title_dirty WHERE book_id=?`, e.id); err != nil {
				return false, err
			}
		} else if e.id > cursor {
			cursor = e.id
		}
		for _, gram := range titleGrams(e.title) {
			if _, err := insert.ExecContext(ctx, gram, e.id); err != nil {
				return false, err
			}
		}
	}
	if cursor > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE book_title_index SET cursor=? WHERE id=1`, cursor); err != nil {
			return false, err
		}
	}
	return len(entries) > 0, tx.Commit()
}

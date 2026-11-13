package library

import (
	"database/sql"
	"strings"
	"talebook/booksvr/internal/bookidentity"
)

// Refresh comparison projections once per rule version. It never reads sources
// or edits metadata. The cursor and enqueue commit together under the worker lease.
func refreshIdentityAuthors(tx *sql.Tx) error {
	var done int
	if err := tx.QueryRow(`SELECT coalesce((SELECT value FROM book_identity_state WHERE key='authors_v5_done'),0)`).Scan(&done); err != nil || done != 0 {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO book_identity_state(key,value) VALUES('authors_v5_max',(SELECT coalesce(max(book_id),0) FROM book_identity_keys))`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT k.book_id,k.author_key,b.author_sort,k.title_key,k.managed_work,b.title,k.reason='conflicting_candidates',EXISTS(SELECT 1 FROM book_identity_keys p WHERE p.isbn=k.isbn AND p.isbn<>'' AND p.title_key=k.title_key AND p.book_id<>k.book_id AND (p.managed_work=0 OR k.managed_work=0 OR p.managed_work<>k.managed_work)) FROM book_identity_keys k JOIN books b ON b.id=k.book_id WHERE k.book_id>coalesce((SELECT value FROM book_identity_state WHERE key='authors_v5_cursor'),0) AND k.book_id<=(SELECT value FROM book_identity_state WHERE key='authors_v5_max') ORDER BY k.book_id LIMIT 200`)
	if err != nil {
		return err
	}
	type entry struct {
		id            int64
		key, fallback string
		rawTitle      string
		conflict      bool
		split         bool
		title         string
		managed       int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var fallback sql.NullString
		if err = rows.Scan(&e.id, &e.key, &fallback, &e.title, &e.managed, &e.rawTitle, &e.conflict, &e.split); err != nil {
			rows.Close()
			return err
		}
		e.fallback = fallback.String
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		_, err = tx.Exec(`INSERT INTO book_identity_state(key,value) VALUES('authors_v5_done',1) ON CONFLICT(key) DO UPDATE SET value=1`)
		return err
	}
	args := make([]any, 0, len(entries))
	marks := make([]string, 0, len(entries))
	for _, e := range entries {
		args = append(args, e.id)
		marks = append(marks, "?")
	}
	rows, err = tx.Query(`SELECT l.book,a.name FROM books_authors_link l JOIN authors a ON a.id=l.author WHERE l.book IN (`+strings.Join(marks, ",")+`) ORDER BY l.book,l.id`, args...)
	if err != nil {
		return err
	}
	authors := map[int64][]string{}
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		authors[id] = append(authors[id], name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		names := authors[e.id]
		if len(names) == 0 {
			names = []string{e.fallback}
		}
		keys := bookidentity.Key(bookidentity.Metadata{Title: e.rawTitle, Authors: names})
		key := keys.Authors
		needed := e.split || e.conflict
		if key != e.key || keys.Title != e.title {
			needed = needed || keys.Title != e.title
			if _, err = tx.Exec(`UPDATE book_identity_keys SET author_key=?,title_key=?,updated_at=unixepoch() WHERE book_id=?`, key, keys.Title, e.id); err != nil {
				return err
			}
			needed = needed || e.managed > 0
			if !needed && key != "" {
				if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM book_identity_keys WHERE title_key=? AND author_key=? AND book_id<>?)`, keys.Title, key, e.id).Scan(&needed); err != nil {
					return err
				}
			}
		}
		if needed {
			if _, err = tx.Exec(`INSERT INTO book_identity_queue(book_id) VALUES(?) ON CONFLICT(book_id) DO UPDATE SET version=version+1,next_at=0`, e.id); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`INSERT INTO book_identity_state(key,value) VALUES('authors_v5_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, entries[len(entries)-1].id)
	return err
}

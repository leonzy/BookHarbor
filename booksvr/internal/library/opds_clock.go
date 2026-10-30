package library

import "database/sql"

// EnsureOpdsClock maintains the catalogue revision without MAX over books.
// The initial revision is this catalogue's creation time, not a claimed book date.
// All triggers use SQLite built-ins, so existing import processes maintain it too.
func EnsureOpdsClock(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS book_opds_state(id INTEGER PRIMARY KEY CHECK(id=1),updated_at INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO book_opds_state(id,updated_at) VALUES(1,unixepoch())`,
	} {
		if _, err = tx.Exec(q); err != nil {
			return err
		}
	}
	for _, table := range []string{"books", "authors", "tags", "publishers", "series", "ratings", "languages", "books_authors_link", "books_tags_link", "books_publishers_link", "books_series_link", "books_ratings_link", "books_languages_link", "data", "book_formats", "book_unavailable_sources"} {
		var exists bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS opds_clock_` + table + `_` + event + ` AFTER ` + event + ` ON ` + table + ` BEGIN UPDATE book_opds_state SET updated_at=unixepoch() WHERE id=1 AND updated_at<unixepoch(); END`); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

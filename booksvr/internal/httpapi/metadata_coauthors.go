package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"talebook/booksvr/internal/library"
	"time"
)

// Discover once using indexed comparison keys. Never traverse source directories.
func (m *metadataWorker) discoverCoauthorRechecks() error {
	db := m.w.libraryDB
	done, err := m.state("coauthors_v1_done")
	if err != nil || done != 0 {
		return err
	}
	if !library.IdentityAvailable(db) {
		return nil
	}
	var ready int
	if err := db.QueryRow(`SELECT coalesce((SELECT value FROM book_identity_state WHERE key='authors_v5_done'),0)`).Scan(&ready); err != nil {
		return err
	}
	if ready == 0 {
		return nil
	}
	if _, err = db.Exec(`INSERT OR IGNORE INTO metadata_enrichment_state(key,value) VALUES('coauthors_v1_max',(SELECT coalesce(max(book_id),0) FROM book_identity_keys))`); err != nil {
		return err
	}
	cursor, err := m.state("coauthors_v1_cursor")
	if err != nil {
		return err
	}
	high, err := m.state("coauthors_v1_max")
	if err != nil {
		return err
	}
	end := cursor + 200
	if end > high {
		end = high
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO metadata_coauthor_rechecks(book_id) SELECT k.book_id FROM book_identity_keys k JOIN books b ON b.id=k.book_id WHERE k.book_id>? AND k.book_id<=? AND k.title_key<>'' AND k.author_key<>'' AND instr(k.author_key,char(31))=0 AND EXISTS(SELECT 1 FROM book_identity_keys p WHERE p.title_key=k.title_key AND p.book_id<>k.book_id AND instr(p.author_key,char(31))>0 AND instr(char(31)||p.author_key||char(31),char(31)||k.author_key||char(31))>0)`, cursor, end); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES('coauthors_v1_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, end); err != nil {
		return err
	}
	if end >= high {
		if _, err = tx.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES('coauthors_v1_done',1) ON CONFLICT(key) DO UPDATE SET value=1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *metadataWorker) recheckCoauthorOne(ctx context.Context) error {
	var id int64
	err := m.w.libraryDB.QueryRow(`SELECT book_id FROM metadata_coauthor_rechecks WHERE status IN ('pending','unreadable') AND next_at<=? ORDER BY next_at,book_id LIMIT 1`, m.now().Unix()).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var protected bool
	if err = m.w.libraryDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM metadata_field_locks WHERE book_id=? AND field IN ('authors','author_sort'))`, id).Scan(&protected); err != nil {
		return err
	}
	status, next := "done", int64(0)
	if protected {
		status = "protected"
	} else {
		if err = m.local(ctx, id, true, true); errors.Is(err, errMetadataSourceUnreadable) {
			status = "unreadable"
			next = m.now().Add(time.Hour).Unix()
		} else if err != nil {
			return err
		}
	}
	_, err = m.w.libraryDB.Exec(`UPDATE metadata_coauthor_rechecks SET status=?,next_at=?,updated_at=? WHERE book_id=?`, status, next, m.now().Unix(), id)
	return err
}

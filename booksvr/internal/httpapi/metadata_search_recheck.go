package httpapi

import (
	"context"
	"database/sql"
	"io"
	"log"
	"strings"
	"time"
	"unicode"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
)

func metadataSearchable(book library.BookData) bool {
	if localmeta.ISBN(book.ISBN) != "" {
		return true
	}
	title := strings.TrimSpace(localmeta.SearchTitle(book.Title))
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// Repair the old pre-search author gate once, using primary-key ranges. At most
// one source file is read per normal tick. Existing healthy source jobs and both
// historical/new-book cursors are retained; manual protection remains in local().
func (m *metadataWorker) recheckSearchEligibility(ctx context.Context) error {
	done, err := m.state("search_identity_v2_done")
	if err != nil || done != 0 {
		return err
	}
	db := m.w.libraryDB
	if _, err = db.Exec(`INSERT OR IGNORE INTO metadata_enrichment_state(key,value) VALUES('search_identity_v2_max',(SELECT coalesce(max(book_id),0) FROM metadata_enrichment_jobs))`); err != nil {
		return err
	}
	cursor, err := m.state("search_identity_v2_cursor")
	if err != nil {
		return err
	}
	high, err := m.state("search_identity_v2_max")
	if err != nil {
		return err
	}
	end := cursor + 200
	if end > high {
		end = high
	}
	var id int64
	err = db.QueryRow(`SELECT book_id FROM metadata_enrichment_jobs NOT INDEXED WHERE book_id>? AND book_id<=? AND status='insufficient' AND last_error IN ('缺少有效 ISBN，且无法确认作者，等待补充身份信息','缺少可用于检索的书名和有效 ISBN') ORDER BY book_id LIMIT 1`, cursor, end).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if err = m.local(ctx, id, true); err != nil {
			return err
		}
		// Only the obsolete author gate outcome is reopened. No completed successful
		// searches, in-flight detail calls or independent retry timers are reset.
		if _, err = db.Exec(`UPDATE metadata_source_jobs SET phase='search',outcome='',last_error='',next_at=0 WHERE book_id=? AND outcome='insufficient' AND EXISTS(SELECT 1 FROM metadata_enrichment_jobs WHERE book_id=? AND status='network_pending')`, id, id); err != nil {
			return err
		}
		end = id
	}
	if err = m.setState("search_identity_v2_cursor", end); err != nil {
		return err
	}
	if end >= high {
		return m.setState("search_identity_v2_done", 1)
	}
	return nil
}

// RefreshFileMetadata is the explicit, bounded maintenance entry point. It uses
// the same field locks and missing-only writes as the normal background worker.
// It does not take over that worker's lease or alter its progress cursors.
func RefreshFileMetadata(ctx context.Context, db *sql.DB, libraryRoot string, id int64) error {
	w := newBookWiring(nil, db, nil, nil)
	w.libraryRoot = libraryRoot
	m := &metadataWorker{w: w, now: time.Now, logger: log.New(io.Discard, "", 0)}
	return m.local(ctx, id, true)
}

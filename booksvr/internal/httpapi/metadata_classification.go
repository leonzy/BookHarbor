package httpapi

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/normalize"
)

// Filename author labels are lookup hints only. No stored or manually cleared
// metadata is overwritten by deriving this temporary search identity.
func metadataLookupBook(book library.BookData) library.BookData {
	book.Authors = normalize.CleanAuthors(book.Authors)
	if localmeta.MissingAuthors(book.Authors) && localmeta.FilenameTitle(book.Title) != book.Title {
		if hint := localmeta.FromText(book.Title); !localmeta.MissingAuthors(hint.Authors) {
			book.Authors = hint.Authors
		}
	}
	return book
}

func metadataReviewCandidates(book library.BookData, records []map[string]any) []map[string]any {
	if matchingMetadata(book, records) != nil {
		return nil
	}
	var candidates []map[string]any
	seen := map[string]bool{}
	for _, record := range records {
		if matchingMetadata(book, []map[string]any{record}) == nil {
			continue
		}
		key := isbn13(stringOf(record["isbn"])) + "\x00" + stringOf(record["provider_value"])
		if seen[key] {
			continue
		}
		seen[key] = true
		// Keep the opaque provider key to distinguish editions; exclude artwork and unbounded descriptions.
		summary := map[string]any{}
		for _, field := range []string{"title", "authors", "publisher", "isbn", "provider_value"} {
			summary[field] = record[field]
		}
		data, err := json.Marshal(summary)
		if err != nil || len(data) > 4096 {
			continue
		}
		candidates = append(candidates, summary)
		if len(candidates) == 5 {
			break
		}
	}
	if len(candidates) < 2 {
		return nil
	}
	return candidates
}

func (m *metadataWorker) setSourceOutcome(t sourceTask, outcome string, candidates []map[string]any) error {
	if candidates == nil {
		candidates = []map[string]any{}
	}
	data, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	_, err = m.w.libraryDB.Exec(`UPDATE metadata_source_jobs SET outcome=?,review_candidates=? WHERE book_id=? AND connection_id=?`, outcome, string(data), t.book, t.connection)
	return err
}

type metadataSourceSummary struct{ total, unfinished, matched, failed, review int }

func sourceSummary(db *sql.DB, id int64) (metadataSourceSummary, error) {
	var out metadataSourceSummary
	rows, err := db.Query(`SELECT phase,matched,last_error,outcome,review_candidates FROM metadata_source_jobs WHERE book_id=?`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var phase, reason, outcome, raw string
		var matched int
		if err = rows.Scan(&phase, &matched, &reason, &outcome, &raw); err != nil {
			return out, err
		}
		out.total++
		if phase != "done" {
			out.unfinished++
		}
		out.matched += matched
		if outcome == "source_failed" || (outcome == "" && (reason == "来源请求失败" || strings.Contains(reason, "缓存上限"))) {
			out.failed++
		}
		var candidates []map[string]any
		if outcome == "ambiguous" && len(raw) <= 32768 && json.Unmarshal([]byte(raw), &candidates) == nil && len(candidates) >= 2 {
			out.review++
		}
	}
	return out, rows.Err()
}

// Each tick repairs at most 50 records in short compare-and-swap transactions.
// The normal import queue, books, manual locks, cursors and healthy source jobs
// are never reset. A concurrent manual save wins through the status predicate.
func (m *metadataWorker) reclassifyReviewBatch() error {
	db := m.w.libraryDB
	var finished int64
	if err := db.QueryRow(`SELECT coalesce((SELECT value FROM metadata_enrichment_state WHERE key='classification_v2_done'),0)`).Scan(&finished); err != nil {
		return err
	}
	if finished != 0 {
		return nil
	}
	cursor, err := m.state("classification_v2_cursor")
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT book_id,last_error,updated_at FROM metadata_enrichment_jobs WHERE status='needs_review' AND book_id>? ORDER BY book_id LIMIT 50`, cursor)
	if err != nil {
		return err
	}
	type oldJob struct {
		id, at int64
		reason string
	}
	var batch []oldJob
	var ids []int64
	for rows.Next() {
		var j oldJob
		if err = rows.Scan(&j.id, &j.reason, &j.at); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, j)
		ids = append(ids, j.id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(batch) == 0 {
		return m.setState("classification_v2_done", 1)
	}
	owners, err := metadataSourceOwners(m.w.db, ids)
	if err != nil {
		return err
	}
	books, err := library.GetBooksData(db, ids)
	if err != nil {
		return err
	}
	byID := map[int64]library.BookData{}
	for _, b := range books {
		byID[b.ID] = metadataLookupBook(b)
	}
	type repair struct {
		old            oldJob
		status, reason string
		delay          time.Duration
	}
	var repairs []repair
	for _, old := range batch {
		b, exists := byID[old.id]
		status, reason, delay := "network_pending", "历史分类修正，等待现有队列继续查询", time.Duration(0)
		if !exists {
			status, reason = "deleted", "书籍已删除"
		} else if owners[old.id] == 0 {
			status, reason = "insufficient", "书籍没有可用的所属用户"
		} else if localmeta.ISBN(b.ISBN) == "" && localmeta.MissingAuthors(b.Authors) {
			status, reason = "insufficient", "缺少有效 ISBN，且无法确认作者，等待补充身份信息"
		} else {
			summary, e := sourceSummary(db, old.id)
			if e != nil {
				return e
			}
			switch {
			case localmeta.ISBN(b.ISBN) != "" && b.Publisher != "" && !localmeta.MissingAuthors(b.Authors) && b.Comments != "":
				status, reason = "complete", ""
			case summary.review > 0:
				status, reason = "needs_review", "存在多个可验证的候选版本，请核对已保存的候选"
			case summary.unfinished > 0 || summary.total == 0:
			case summary.failed > 0:
				status, reason, delay = "source_failed", "来源请求失败，等待独立退避重试", time.Hour
			case summary.matched > 0:
				status, reason = "partial", "已取得匹配信息，仍缺少部分字段"
			default:
				status, reason = "unmatched", "所有已查询来源均未取得可靠匹配"
			}
		}
		repairs = append(repairs, repair{old, status, reason, delay})
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, item := range repairs {
		old, status, reason, delay := item.old, item.status, item.reason, item.delay
		result, e := tx.Exec(`UPDATE metadata_enrichment_jobs SET status=?,last_error=?,next_at=?,updated_at=? WHERE book_id=? AND status='needs_review' AND updated_at=?`, status, reason, m.now().Add(delay).Unix(), m.now().Unix(), old.id, old.at)
		if e != nil {
			return e
		}
		changed, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if changed > 0 && status == "insufficient" {
			if _, e = tx.Exec(`UPDATE metadata_source_jobs SET phase='done',outcome='insufficient',review_candidates='[]',next_at=0 WHERE book_id=?`, old.id); e != nil {
				return e
			}
		}
		if changed > 0 {
			if _, e = tx.Exec(`UPDATE metadata_source_jobs SET outcome=CASE WHEN last_error='来源请求失败' OR last_error LIKE '%缓存上限%' THEN 'source_failed' WHEN matched>0 THEN 'matched' ELSE 'no_match' END WHERE book_id=? AND phase='done' AND outcome=''`, old.id); e != nil {
				return e
			}
		}
	}
	if _, e = tx.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES('classification_v2_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, batch[len(batch)-1].id); e != nil {
		return e
	}
	return tx.Commit()
}

// Retry only terminal failed sources. Successful and empty sources retain their
// outcomes; all requests still pass through the existing per-source limiter.
func (m *metadataWorker) retryFailedSourceBook() error {
	var id int64
	err := m.w.libraryDB.QueryRow(`SELECT book_id FROM metadata_enrichment_jobs WHERE status='source_failed' AND next_at<=? ORDER BY next_at,book_id LIMIT 1`, m.now().Unix()).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := m.w.libraryDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE metadata_source_jobs SET phase='search',attempts=0,matched=0,record='{}',review_candidates='[]',outcome='',last_error='',next_at=max(next_at,?) WHERE book_id=? AND outcome='source_failed' AND EXISTS(SELECT 1 FROM metadata_enrichment_jobs WHERE book_id=? AND status='source_failed')`, m.now().Unix(), id, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE metadata_enrichment_jobs SET status='network_pending',last_error='',updated_at=? WHERE book_id=? AND status='source_failed'`, m.now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

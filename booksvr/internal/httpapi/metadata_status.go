package httpapi

import (
	"database/sql"
	"strings"
	"time"
)

const metadataJobsStatusSQL = `SELECT j.status,count(*),max(j.updated_at) FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id GROUP BY j.status`

// Materialize only failed source candidates before joining the checked-book
// queue. Starting from all pending books multiplies work by every source.
const metadataRetryCountSQL = `SELECT count(*) FROM
	(SELECT DISTINCT book_id FROM metadata_source_jobs
	 WHERE phase<>'done' AND attempts>0 AND (outcome='source_failed' OR last_error='来源请求失败')) s
	JOIN books b ON b.id=s.book_id JOIN metadata_enrichment_jobs j ON j.book_id=s.book_id
	WHERE j.status IN ('network_pending','partial')`

const metadataNetworkWriteCountSQL = `SELECT count(DISTINCT w.book_id) FROM metadata_enrichment_writes w JOIN books b ON b.id=w.book_id WHERE w.origin LIKE 'network:%'`

// Build once at worker initialization, never in a progress request. Covering
// indexes avoid fetching large job/write payloads merely to count them.
func ensureMetadataProgressIndexes(db *sql.DB) error {
	for _, query := range []string{
		`CREATE INDEX IF NOT EXISTS metadata_progress_status ON metadata_enrichment_jobs(status,updated_at,book_id)`,
		`CREATE INDEX IF NOT EXISTS metadata_progress_retry ON metadata_source_jobs(book_id) WHERE phase<>'done' AND attempts>0 AND (outcome='source_failed' OR last_error='来源请求失败')`,
		`CREATE INDEX IF NOT EXISTS metadata_progress_network_writes ON metadata_enrichment_writes(origin COLLATE NOCASE,book_id)`,
	} {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func MetadataEnrichmentStatus(db *sql.DB, opts ServeOptions, userDB ...*sql.DB) HandlerFunc {
	return func(ctx *Context) (map[string]any, error) {
		var total, checked, latest, applied, retries int64
		if e := db.QueryRow(`SELECT count(*) FROM books`).Scan(&total); e != nil {
			return nil, e
		}
		rows, e := db.Query(metadataJobsStatusSQL)
		if e != nil {
			return nil, e
		}
		counts := map[string]int64{}
		for rows.Next() {
			var status string
			var n, at int64
			if e = rows.Scan(&status, &n, &at); e != nil {
				rows.Close()
				return nil, e
			}
			counts[status] = n
			checked += n
			if at > latest {
				latest = at
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if e = db.QueryRow(metadataRetryCountSQL).Scan(&retries); e != nil {
			return nil, e
		}
		if e = db.QueryRow(metadataNetworkWriteCountSQL).Scan(&applied); e != nil {
			return nil, e
		}
		var owner string
		var expires int64
		e = db.QueryRow(`SELECT owner,expires FROM metadata_enrichment_lease WHERE id=1`).Scan(&owner, &expires)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		settings := opts.Settings
		if opts.SettingsPath != "" {
			settings = LoadSettings(opts.SettingsPath)
		}
		remaining := total - checked
		if remaining < 0 {
			remaining = 0
		}
		var refresh, written int64
		if e = db.QueryRow(`SELECT count(*) FROM metadata_enrichment_jobs WHERE status='file_retry' AND last_error='TXT 简介规则更新，等待限速文件复核'`).Scan(&refresh); e != nil {
			return nil, e
		}
		counts["file_refresh"] = refresh
		var verified int64
		if e = db.QueryRow(`SELECT count(*) FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id WHERE j.status='needs_review' AND ` + verifiedMetadataReviewSQL).Scan(&verified); e != nil {
			return nil, e
		}
		counts["classification_pending"] = counts["needs_review"] - verified
		counts["needs_review"] = verified
		retries += counts["source_failed"]

		if e = db.QueryRow(`SELECT count(DISTINCT w.book_id) FROM metadata_enrichment_writes w JOIN books b ON b.id=w.book_id`).Scan(&written); e != nil {
			return nil, e
		}
		var udb *sql.DB
		if len(userDB) > 0 {
			udb = userDB[0]
		}
		sources, e := metadataSourceStatus(db, udb, opts, ctx)
		if e != nil {
			return nil, e
		}
		lecture, e := lectureRecheckStatus(db)
		if e != nil {
			return nil, e
		}
		return map[string]any{"err": "ok", "total_books": total, "files_checked": checked, "files_remaining": remaining, "counts": counts, "network_retries": retries, "network_applied_books": applied, "last_activity_at": latest, "worker_active": expires > time.Now().Unix() && !strings.HasPrefix(owner, "maintenance:") && !strings.HasPrefix(owner, "deployment:"), "network_enabled": settingBool(settings, "auto_fill_meta", false), "local_interval_seconds": 1, "network_interval_seconds": 0, "source_default_interval_seconds": 5, "network_max_concurrency": metadataSourceConcurrency, "sources": sources, "written_books": written, "overwrite_existing": false, "lecture_recheck": lecture}, nil
	}
}

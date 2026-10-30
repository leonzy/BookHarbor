package httpapi

import (
	"database/sql"
	"strings"
	"time"
)

func MetadataEnrichmentStatus(db *sql.DB, opts ServeOptions) HandlerFunc {
	return func(ctx *Context) (map[string]any, error) {
		var total, checked, latest, applied, retries int64
		if e := db.QueryRow(`SELECT count(*) FROM books`).Scan(&total); e != nil {
			return nil, e
		}
		rows, e := db.Query(`SELECT j.status,count(*),max(j.updated_at) FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id GROUP BY j.status`)
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
		if e = db.QueryRow(`SELECT count(*) FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id WHERE j.status='network_pending' AND j.attempts>0`).Scan(&retries); e != nil {
			return nil, e
		}
		if e = db.QueryRow(`SELECT count(DISTINCT w.book_id) FROM metadata_enrichment_writes w JOIN books b ON b.id=w.book_id WHERE w.origin LIKE 'network:%'`).Scan(&applied); e != nil {
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
		return map[string]any{"err": "ok", "total_books": total, "files_checked": checked, "files_remaining": remaining, "counts": counts, "network_retries": retries, "network_applied_books": applied, "last_activity_at": latest, "worker_active": expires > time.Now().Unix() && !strings.HasPrefix(owner, "maintenance:") && !strings.HasPrefix(owner, "deployment:"), "network_enabled": settingBool(settings, "auto_fill_meta", false), "local_interval_seconds": 1, "network_interval_seconds": 30, "overwrite_existing": false}, nil
	}
}

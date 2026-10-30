package httpapi

import (
	"database/sql"
	"strconv"
)

const rMetadataReview = "/api/admin/metadata/enrichment/review"

// Review is read-only. Explicit choices continue through protected book edits.
func metadataReviewRoutes(db *sql.DB) []RouteSpec {
	return []RouteSpec{{Pattern: rMetadataReview, Methods: []string{"GET"}, Auth: true, Admin: true, Handler: func(ctx *Context) (map[string]any, error) {
		after := int64(0)
		if raw := ctx.Request.URL.Query().Get("after"); raw != "" {
			var err error
			after, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || after < 0 {
				return map[string]any{"err": "params.invalid"}, nil
			}
		}
		rows, err := db.Query(`SELECT j.book_id,b.title,j.last_error FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id WHERE j.status='needs_review' AND j.book_id>? ORDER BY j.book_id LIMIT 21`, after)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []map[string]any{}
		next := int64(0)
		for rows.Next() {
			var id int64
			var title, reason string
			if err = rows.Scan(&id, &title, &reason); err != nil {
				return nil, err
			}
			if len(items) == 20 {
				next = items[19]["id"].(int64)
				break
			}
			items = append(items, map[string]any{"id": id, "title": title, "reason": reason})
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		return map[string]any{"err": "ok", "books": items, "next_after": next}, nil
	}}}
}

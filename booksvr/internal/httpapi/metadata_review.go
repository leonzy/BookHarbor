package httpapi

import (
	"database/sql"
	"encoding/json"
	"strconv"
)

const verifiedMetadataReviewSQL = `EXISTS(SELECT 1 FROM metadata_source_jobs s WHERE s.book_id=j.book_id AND s.outcome='ambiguous' AND CASE WHEN json_valid(s.review_candidates) THEN json_array_length(s.review_candidates) ELSE 0 END>=2)`

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
		rows, err := db.Query(`SELECT j.book_id,b.title,j.last_error FROM metadata_enrichment_jobs j JOIN books b ON b.id=j.book_id WHERE j.status='needs_review' AND j.book_id>? AND `+verifiedMetadataReviewSQL+` ORDER BY j.book_id LIMIT 21`, after)
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

		rows.Close()
		for _, item := range items {
			evidence, e := db.Query(`SELECT review_candidates FROM metadata_source_jobs WHERE book_id=? AND outcome='ambiguous' ORDER BY connection_id`, item["id"])
			if e != nil {
				return nil, e
			}
			var candidates []map[string]any
			for evidence.Next() {
				var raw string
				if e = evidence.Scan(&raw); e != nil {
					evidence.Close()
					return nil, e
				}
				var list []map[string]any
				if len(raw) <= 32768 && json.Unmarshal([]byte(raw), &list) == nil {
					for _, candidate := range list {
						if len(candidates) < 5 {
							candidates = append(candidates, candidate)
						}
					}
				}
			}
			e = evidence.Err()
			evidence.Close()
			if e != nil {
				return nil, e
			}
			item["candidates"] = candidates
		}
		return map[string]any{"err": "ok", "books": items, "next_after": next}, nil
	}}}
}

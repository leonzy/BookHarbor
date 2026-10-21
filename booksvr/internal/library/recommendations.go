package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// RecommendationIDs selects bounded metadata candidates, never complete file views.
// Each affinity relation uses its book/item indexes; the final visibility filter
// runs before scoring so a private or missing title cannot influence a profile.
func RecommendationIDs(ctx context.Context, db *sql.DB, seeds []int64) ([]int64, error) {
	ids := []int64{}
	seen := map[int64]bool{}
	add := func(query string, args ...any) error {
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return rows.Err()
	}
	if err := add(`SELECT id FROM books ORDER BY timestamp DESC,id DESC LIMIT 512`); err != nil {
		return nil, err
	}
	if len(seeds) > 0 {
		raw, _ := json.Marshal(seeds)
		for _, pair := range [][2]string{{"books_authors_link", "author"}, {"books_tags_link", "tag"}, {"books_series_link", "series"}} {
			q := fmt.Sprintf(`SELECT DISTINCT book FROM %s WHERE %s IN (SELECT %s FROM %s WHERE book IN (SELECT value FROM json_each(?))) ORDER BY book DESC LIMIT 512`, pair[0], pair[1], pair[1], pair[0])
			if err := add(q, string(raw)); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

// RecommendationMetadata deliberately omits comments, paths, files and covers.
func RecommendationMetadata(ctx context.Context, db *sql.DB, ids []int64) ([]BookData, error) {
	raw, _ := json.Marshal(ids)
	rows, err := db.QueryContext(ctx, `SELECT b.id,COALESCE(b.timestamp,0),
 COALESCE((SELECT json_group_array(a.name) FROM books_authors_link l JOIN authors a ON a.id=l.author WHERE l.book=b.id),'[]'),
 COALESCE((SELECT json_group_array(t.name) FROM books_tags_link l JOIN tags t ON t.id=l.tag WHERE l.book=b.id),'[]'),
 COALESCE((SELECT s.name FROM books_series_link l JOIN series s ON s.id=l.series WHERE l.book=b.id LIMIT 1),''),
 COALESCE((SELECT r.rating FROM books_ratings_link l JOIN ratings r ON r.id=l.rating WHERE l.book=b.id LIMIT 1),0)
 FROM books b WHERE b.id IN (SELECT value FROM json_each(?))`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookData{}
	for rows.Next() {
		var b BookData
		var stamp sql.RawBytes
		var a, t string
		if err := rows.Scan(&b.ID, &stamp, &a, &t, &b.Series, &b.Rating); err != nil {
			return nil, err
		}
		b.Timestamp = parseCalibreTimestamp(stamp)
		if err := json.Unmarshal([]byte(a), &b.Authors); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(t), &b.Tags); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

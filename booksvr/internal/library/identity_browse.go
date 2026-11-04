package library

import (
	"context"
	"database/sql"
	"strings"
)

// Only confirmed automatic groups collapse. Apply the same visibility and
// filter predicate to the alternative representative BEFORE count and paging.
func identityHiddenSQL(ctx context.Context, db *sql.DB, where string) (string, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='book_identity_keys'`).Scan(&n); err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	// Materialize only confirmed group members once. Putting the filter inside
	// each correlated lookup lets SQLite rebuild a large format/category set
	// once per group member, which is catastrophic for a large catalog.
	return `WITH matching AS MATERIALIZED(SELECT k.book_id,k.managed_work FROM book_identity_keys k INDEXED BY book_identity_group CROSS JOIN books b ON b.id=k.book_id AND b.work_id=k.managed_work WHERE k.managed_work>0 AND (` + where + `)) SELECT k.book_id FROM book_identity_keys k INDEXED BY book_identity_group CROSS JOIN books g ON g.id=k.book_id AND g.work_id=k.managed_work WHERE k.managed_work>0 AND EXISTS(SELECT 1 FROM matching p WHERE p.managed_work=k.managed_work AND p.book_id<g.id)`, nil
}

// IdentityCardFormats deliberately loads no titles, covers or large comments.
// Reuse the existing authoritative format/missing-source logic in bounded batches.
func IdentityCardFormats(db *sql.DB, ids []int64) (map[int64]*BookData, error) {
	out := map[int64]*BookData{}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		args := []any{}
		marks := []string{}
		for _, id := range ids[start:end] {
			out[id] = &BookData{ID: id, FormatSizes: map[string]int64{}}
			args = append(args, id)
			marks = append(marks, "?")
		}
		if err := attachFormats(db, out, strings.Join(marks, ","), args); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CollapseIdentityIDs preserves relevance order and uses only the caller's
// already visible candidate set. A private or out-of-scope sibling cannot hide it.
func CollapseIdentityIDs(db *sql.DB, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	groups, err := IdentityCardMembers(db, ids)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		out = append(out, id)
		for _, member := range groups[id] {
			seen[member] = true
		}
		seen[id] = true
	}
	return out, nil
}

// IdentityCardMembers is a single indexed, bounded query for one rendered page.
func IdentityCardMembers(db *sql.DB, ids []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='book_identity_keys'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 || len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(`SELECT r.id,b.id FROM books r JOIN book_identity_keys rk ON rk.book_id=r.id AND rk.managed_work=r.work_id AND rk.managed_work>0 JOIN books b ON b.work_id=r.work_id JOIN book_identity_keys k ON k.book_id=b.id AND k.managed_work=r.work_id WHERE r.id IN (SELECT value FROM json_each(?)) AND b.id IN (SELECT id FROM books WHERE work_id=r.work_id ORDER BY id LIMIT 64) ORDER BY r.id,b.id`, jsonIDs(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var root, id int64
		if err = rows.Scan(&root, &id); err != nil {
			return nil, err
		}
		out[root] = append(out[root], id)
	}
	return out, rows.Err()
}

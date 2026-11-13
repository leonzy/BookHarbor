package library

import (
	"context"
	"database/sql"
)

func AccessTagCounts(ctx context.Context, db *sql.DB, scope AccessScope) (map[string]int, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	rows, err := db.QueryContext(ctx, `SELECT t.name,sum(c.n) FROM acl_facet_count c JOIN tags t ON t.id=CAST(c.item AS INTEGER) WHERE c.revision=? AND c.profile=? AND c.kind='tags' AND c.owner IN ('',?) GROUP BY c.item HAVING sum(c.n)>0`, scope.revision, scope.profile, scope.principal)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err = rows.Scan(&name, &count); err != nil {
			rows.Close()
			return nil, err
		}
		out[name] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, (AccessStore{DB: db}).AccessCurrent(ctx, scope)
}

// AccessFilterIDs keeps caller order and queries only the explicit candidate
// set, in bounded batches. It does not materialize all authorised book IDs.
func AccessFilterIDs(ctx context.Context, db *sql.DB, scope AccessScope, ids []int64) ([]int64, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	out := []int64{}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		rows, err := db.QueryContext(ctx, `SELECT book_id FROM acl_profile_book WHERE revision=? AND profile=? AND owner IN ('',?) AND book_id IN (SELECT value FROM json_each(?))`, scope.revision, scope.profile, scope.principal, jsonIDs(ids[start:end]))
		if err != nil {
			return nil, err
		}
		allowed := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			allowed[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, id := range ids[start:end] {
			if allowed[id] {
				out = append(out, id)
			}
		}
	}
	return out, (AccessStore{DB: db}).AccessCurrent(ctx, scope)
}

func AccessSummary(ctx context.Context, db *sql.DB, scope AccessScope) (map[string]int64, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	out := map[string]int64{"books": 0, "authors": 0, "publishers": 0, "tags": 0, "series": 0, "formats": 0}
	var books int64
	if err := db.QueryRowContext(ctx, `SELECT coalesce(sum(books),0) FROM acl_profile_count WHERE revision=? AND profile=? AND owner IN ('',?)`, scope.revision, scope.profile, scope.principal).Scan(&books); err != nil {
		return nil, err
	}
	out["books"] = books
	rows, err := db.QueryContext(ctx, `SELECT kind,count(*) FROM (SELECT kind,item FROM acl_facet_count WHERE revision=? AND profile=? AND owner IN ('',?) GROUP BY kind,item HAVING sum(n)>0) GROUP BY kind`, scope.revision, scope.profile, scope.principal)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var count int64
		if err = rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return nil, err
		}
		out[kind] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, (AccessStore{DB: db}).AccessCurrent(ctx, scope)
}

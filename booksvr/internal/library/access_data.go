package library

import (
	"context"
	"database/sql"
	"fmt"
)

// AccessBooksData is for a bounded page or explicit selection, never all IDs in
// the library. Permission is applied before loading descriptions and metadata.
// Callers must obtain the page/count through AccessBrowseBooks, not filter a
// globally paginated page afterwards.
func AccessBooksData(ctx context.Context, db *sql.DB, scope AccessScope, ids []int64) ([]BookData, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	if len(ids) > 500 {
		return nil, fmt.Errorf("access detail batch exceeds budget")
	}
	rows, err := db.QueryContext(ctx, `SELECT book_id,format,MAX(size) FROM acl_profile_file WHERE revision=? AND profile=? AND owner IN ('',?) AND book_id IN (SELECT value FROM json_each(?)) GROUP BY book_id,format ORDER BY book_id,format`, scope.revision, scope.profile, scope.principal, jsonIDs(ids))
	if err != nil {
		return nil, err
	}
	formats := map[int64][]string{}
	sizes := map[int64]map[string]int64{}
	for rows.Next() {
		var id, size int64
		var format string
		if err = rows.Scan(&id, &format, &size); err != nil {
			rows.Close()
			return nil, err
		}
		formats[id] = append(formats[id], format)
		if sizes[id] == nil {
			sizes[id] = map[string]int64{}
		}
		sizes[id][format] = size
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	visible := make([]int64, 0, len(ids))
	for _, id := range ids {
		if len(formats[id]) > 0 {
			visible = append(visible, id)
		}
	}
	books, err := GetBooksData(db, visible)
	if err != nil {
		return nil, err
	}
	for i := range books {
		b := &books[i]
		b.Formats = formats[b.ID]
		b.FormatSizes = sizes[b.ID]
		b.Path = ""
	}
	if err = (AccessStore{DB: db}).AccessCurrent(ctx, scope); err != nil {
		return nil, err
	}
	return books, nil
}

// AccessIdentityCardMembers limits each work only after visibility filtering.
// Hidden older editions cannot consume the 64-version display budget.
func AccessIdentityCardMembers(ctx context.Context, db *sql.DB, scope AccessScope, ids []int64) (map[int64][]int64, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	if len(ids) > 100 {
		return nil, fmt.Errorf("access work batch exceeds budget")
	}
	query := `WITH visible AS (SELECT r.id root,b.id,ROW_NUMBER() OVER(PARTITION BY r.id ORDER BY b.id) position FROM books r JOIN acl_profile_book rp ON rp.book_id=r.id AND rp.revision=? AND rp.profile=? AND rp.owner IN ('',?) JOIN book_identity_keys rk ON rk.book_id=r.id AND rk.managed_work=r.work_id AND rk.managed_work>0 JOIN books b ON b.work_id=r.work_id JOIN book_identity_keys k ON k.book_id=b.id AND k.managed_work=r.work_id JOIN acl_profile_book p ON p.book_id=b.id AND p.revision=rp.revision AND p.profile=rp.profile AND p.owner IN ('',?) WHERE r.id IN (SELECT value FROM json_each(?))) SELECT root,id FROM visible WHERE position<=64 ORDER BY root,id`
	rows, err := db.QueryContext(ctx, query, scope.revision, scope.profile, scope.principal, scope.principal, jsonIDs(ids))
	if err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	for rows.Next() {
		var root, id int64
		if err = rows.Scan(&root, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[root] = append(out[root], id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = (AccessStore{DB: db}).AccessCurrent(ctx, scope); err != nil {
		return nil, err
	}
	return out, nil
}

func AccessIdentityCardFormats(ctx context.Context, db *sql.DB, scope AccessScope, ids []int64) (map[int64]*BookData, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	if len(ids) > 6400 {
		return nil, fmt.Errorf("access format batch exceeds budget")
	}
	out := map[int64]*BookData{}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		rows, err := db.QueryContext(ctx, `SELECT book_id,format,MAX(size) FROM acl_profile_file WHERE revision=? AND profile=? AND owner IN ('',?) AND book_id IN (SELECT value FROM json_each(?)) GROUP BY book_id,format ORDER BY book_id,format`, scope.revision, scope.profile, scope.principal, jsonIDs(ids[start:end]))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, size int64
			var format string
			if err = rows.Scan(&id, &format, &size); err != nil {
				rows.Close()
				return nil, err
			}
			b := out[id]
			if b == nil {
				b = &BookData{ID: id, FormatSizes: map[string]int64{}}
				out[id] = b
			}
			b.Formats = append(b.Formats, format)
			b.FormatSizes[format] = size
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := (AccessStore{DB: db}).AccessCurrent(ctx, scope); err != nil {
		return nil, err
	}
	return out, nil
}

package library

import (
	"context"
	"database/sql"
	"strings"
)

type sourceStateUpdate struct {
	id              int64
	path            string
	file            sourceDiskFile
	missing         int
	previousMissing int
	statOnly        bool
}

// The driver recompiles a Go prepared statement on Exec. Batch state changes
// explicitly, and keep stat-only updates out of availability trigger programs.
// Six parameters per row, at most 768: also safe with a 999-variable limit.
const sourceStateBatchSize = 128

func applySourceStates(ctx context.Context, tx *sql.Tx, updates []sourceStateUpdate) (missing, restored int, err error) {
	var stats, availability []sourceStateUpdate
	for _, u := range updates {
		if u.statOnly {
			stats = append(stats, u)
		} else {
			availability = append(availability, u)
		}
	}
	apply := func(batch []sourceStateUpdate, statOnly bool) (map[int64]bool, error) {
		args := make([]any, 0, len(batch)*6)
		for _, u := range batch {
			args = append(args, u.id, u.path, u.file.key, u.file.size, u.file.mtime, u.missing)
		}
		values := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?),", len(batch)), ",")
		query := `WITH expected(format_id,path,identity,size,mtime,missing) AS (VALUES ` + values + `) `
		if statOnly {
			query += `UPDATE source_sync_formats AS s SET identity=v.identity,size=v.size,mtime=v.mtime FROM expected v WHERE s.format_id=v.format_id AND EXISTS(SELECT 1 FROM book_formats f WHERE f.id=v.format_id AND f.path=v.path) RETURNING format_id`
		} else {
			query += `INSERT INTO source_sync_formats(format_id,identity,size,mtime,missing) SELECT v.format_id,v.identity,v.size,v.mtime,v.missing FROM expected v JOIN book_formats f ON f.id=v.format_id AND f.path=v.path WHERE 1 ON CONFLICT(format_id) DO UPDATE SET identity=excluded.identity,size=excluded.size,mtime=excluded.mtime,missing=excluded.missing RETURNING format_id`
		}
		rows, e := tx.QueryContext(ctx, query, args...)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		applied := make(map[int64]bool, len(batch))
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				return nil, e
			}
			applied[id] = true
		}
		return applied, rows.Err()
	}
	for len(stats) > 0 {
		n := min(len(stats), sourceStateBatchSize)
		applied, e := apply(stats[:n], true)
		if e != nil {
			return 0, 0, e
		}
		for _, u := range stats[:n] {
			if !applied[u.id] {
				// A concurrent format removal is rejected again by the path
				// guard; a removed baseline can be inserted safely if it lives.
				availability = append(availability, u)
			}
		}
		stats = stats[n:]
	}
	for len(availability) > 0 {
		n := min(len(availability), sourceStateBatchSize)
		applied, e := apply(availability[:n], false)
		if e != nil {
			return 0, 0, e
		}
		for _, u := range availability[:n] {
			if applied[u.id] && u.missing != u.previousMissing {
				if u.missing == 1 {
					missing++
				} else {
					restored++
				}
			}
		}
		availability = availability[n:]
	}
	return missing, restored, nil
}

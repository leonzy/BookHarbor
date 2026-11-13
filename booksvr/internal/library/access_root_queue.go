package library

import (
	"context"
	"database/sql"
	"strconv"
)

// Replay only the changed root's indexed identities, in bounded transactions.
// No source files are deleted or restatted and an interrupted replay resumes.
func (s AccessStore) RefreshAccessRootBatch(ctx context.Context) (bool, error) {
	var root string
	if err := s.DB.QueryRowContext(ctx, `SELECT root FROM acl_root_pending LIMIT 1`).Scan(&root); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_root_pending SET cursor=cursor WHERE root=?`, root); err != nil {
		return false, err
	}
	var cursor string
	if err = tx.QueryRowContext(ctx, `SELECT cursor FROM acl_root_pending WHERE root=?`, root).Scan(&cursor); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.id,coalesce(f.book_id,d.book,0) FROM acl_source_identity i LEFT JOIN book_formats f ON substr(i.source_key,1,2)='f:' AND f.id=CAST(substr(i.source_key,3) AS INTEGER) LEFT JOIN data d ON substr(i.source_key,1,2)='d:' AND d.rowid=CAST(substr(i.source_key,3) AS INTEGER) WHERE i.root=? AND i.id>? ORDER BY i.id LIMIT 32`, root, cursor)
	if err != nil {
		return false, err
	}
	ids := map[int64]bool{}
	n := 0
	for rows.Next() {
		var id int64
		if err = rows.Scan(&cursor, &id); err != nil {
			rows.Close()
			return false, err
		}
		n++
		if id > 0 {
			ids[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for id := range ids {
		if _, err = tx.ExecContext(ctx, accessInvalidationSQL(strconv.FormatInt(id, 10))); err != nil {
			return false, err
		}
	}
	if n < 32 {
		_, err = tx.ExecContext(ctx, `DELETE FROM acl_root_pending WHERE root=?`, root)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE acl_root_pending SET cursor=? WHERE root=?`, cursor, root)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

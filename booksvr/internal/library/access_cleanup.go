package library

import (
	"context"
	"database/sql"
)

// Reclaim only obsolete projections in short transactions. Policy, class
// membership and immutable origin records remain available to descendants.
func (s AccessStore) CleanupAccessBatch(ctx context.Context) (bool, error) {
	var revision int64
	err := s.DB.QueryRowContext(ctx, `SELECT r.id FROM acl_revision r WHERE r.status IN ('superseded','cancelled','failed') AND r.id<>(SELECT active_revision FROM acl_state WHERE id=1) AND EXISTS(SELECT 1 FROM acl_profile_book b WHERE b.revision=r.id) ORDER BY r.id LIMIT 1`).Scan(&revision)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Serialize with retries/publication and recheck eligibility under the lock.
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.profile,b.book_id FROM acl_profile_book b JOIN acl_revision r ON r.id=b.revision WHERE b.revision=? AND r.status IN ('superseded','cancelled','failed') AND r.id<>(SELECT active_revision FROM acl_state WHERE id=1) LIMIT 32`, revision)
	if err != nil {
		return false, err
	}
	type item struct {
		profile string
		id      int64
	}
	batch := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.profile, &i.id); err != nil {
			rows.Close()
			return false, err
		}
		batch = append(batch, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, i := range batch {
		if err = removeAccessBook(ctx, tx, revision, i.profile, i.id); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return len(batch) > 0, nil
}

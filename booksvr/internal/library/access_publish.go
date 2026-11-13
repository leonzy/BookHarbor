package library

import (
	"context"
	"time"
)

// PublishAccess is the final atomic switch after the administrator has reviewed
// this exact version and source watermark. Runtime capability checks belong to
// the authenticated controller and must run before calling this method.
func (s AccessStore) PublishAccess(ctx context.Context, id, version, reviewedWatermark int64, actor string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_revision r JOIN acl_build b ON b.revision=r.id AND b.version=r.version JOIN acl_state s ON s.id=1 WHERE r.id=? AND r.version=? AND r.status='ready' AND r.base_revision=s.active_revision AND r.watermark=? AND r.watermark=s.source_watermark AND b.phase='delta' AND NOT EXISTS(SELECT 1 FROM acl_dirty WHERE version>b.watermark) AND EXISTS(SELECT 1 FROM acl_principal WHERE id=? AND disabled=0) AND NOT EXISTS(SELECT 1 FROM acl_principal u WHERE u.disabled=0 AND NOT EXISTS(SELECT 1 FROM acl_profile_principal m WHERE m.revision=r.id AND m.principal=u.id)))`, id, version, reviewedWatermark, actor).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrAccessConflict
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_root_pending)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrAccessNotReady
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_revision SET status='superseded',updated_at=? WHERE id=(SELECT active_revision FROM acl_state WHERE id=1)`, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE acl_revision SET status='active',updated_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET active_revision=?,policy_epoch=policy_epoch+1 WHERE id=1`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO acl_audit(revision,actor,action,created_at) VALUES(?,?,'publish',?)`, id, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

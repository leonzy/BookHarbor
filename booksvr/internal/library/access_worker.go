package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"talebook/booksvr/internal/access"
)

// AccessSnapshotReader must use the supplied read transaction for all metadata
// and source identities. Filesystem health belongs in the shared source-health
// monitor, never a full filesystem scan inside this callback.
type AccessSnapshotReader func(context.Context, *sql.Tx, []int64) ([]AccessBookSnapshot, error)

type AccessWorker struct {
	Store      AccessStore
	Read       AccessSnapshotReader
	BatchSize  int
	OnError    func(error)
	ReadWrites bool
}

// Run maintains at most one pending revision plus the active revision, yielding
// the SQLite writer between every batch. The active profile is serviced first
// so preparing a large draft cannot starve ongoing import visibility updates.
func (w AccessWorker) Run(ctx context.Context) {
	delay := time.Duration(0)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		worked, err := w.Tick(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if w.OnError != nil {
				w.OnError(err)
			}
			delay = 5 * time.Second
		} else if worked {
			delay = 50 * time.Millisecond
		} else {
			delay = 5 * time.Second
		}
	}
}

func (w AccessWorker) Tick(ctx context.Context) (bool, error) {
	rows, err := w.Store.DB.QueryContext(ctx, `SELECT id FROM acl_revision WHERE id=(SELECT active_revision FROM acl_state WHERE id=1) UNION ALL SELECT id FROM (SELECT id FROM acl_revision WHERE status IN ('preparing','ready') AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1) ORDER BY id DESC LIMIT 1)`)
	if err != nil {
		return false, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	worked := false
	for _, id := range ids {
		done, e := w.Step(ctx, id)
		if e != nil {
			return worked, e
		}
		worked = worked || done
	}
	cleaned, err := w.Store.CleanupAccessBatch(ctx)
	return worked || cleaned, err
}

type accessCheckpoint struct {
	Version, Cursor, Ceiling, Watermark int64
	Phase                               string
}

func (s AccessStore) loadAccessProfiles(ctx context.Context, r AccessRevision) (map[string]access.Compiled, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT principal,profile FROM acl_profile_principal WHERE revision=?`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := map[string]access.Compiled{}
	for rows.Next() {
		var principal, key string
		if err = rows.Scan(&principal, &key); err != nil {
			return nil, err
		}
		if _, ok := profiles[key]; ok {
			continue
		}
		c := r.Policy.Compile(principal, false)
		if c.Key() != key {
			return nil, ErrAccessConflict
		}
		profiles[key] = c
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	baseline := r.Policy.Compile("", false)
	var exists bool
	if err = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_profile WHERE revision=? AND profile=?)`, r.ID, baseline.Key()).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		profiles[baseline.Key()] = baseline
	}
	return profiles, nil
}

// Step performs at most one bounded batch. Its persistent high-water marks
// make retries and restarts idempotent; an idle active revision causes no writes.
func (w AccessWorker) Step(ctx context.Context, revision int64) (bool, error) {
	var idle bool
	if err := w.Store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_revision r JOIN acl_build b ON b.revision=r.id AND b.version=r.version JOIN acl_state s ON s.id=1 WHERE r.id=? AND r.status IN ('ready','active') AND b.phase='delta' AND b.watermark=s.source_watermark AND (r.status='active' OR r.watermark=s.source_watermark))`, revision).Scan(&idle); err != nil {
		return false, err
	}
	if idle {
		return false, nil
	}
	lease, err := accessID()
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	res, err := w.Store.DB.ExecContext(ctx, `INSERT INTO acl_worker_lease(id,owner,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE acl_worker_lease.expires<=?`, lease, now+30000, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = w.Store.DB.ExecContext(releaseCtx, `DELETE FROM acl_worker_lease WHERE id=1 AND owner=?`, lease)
	}()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return w.step(ctx, revision, lease)
}

func (w AccessWorker) step(ctx context.Context, revision int64, lease string) (bool, error) {
	if w.Read == nil {
		return false, fmt.Errorf("access snapshot reader unavailable")
	}
	limit := w.BatchSize
	if limit <= 0 {
		limit = 32
	}
	if limit > 64 {
		return false, fmt.Errorf("access batch exceeds budget")
	}
	r, err := w.Store.Revision(ctx, revision)
	if err != nil {
		return false, err
	}
	if r.Status != "preparing" && r.Status != "active" && r.Status != "ready" {
		return false, ErrAccessConflict
	}
	var cp accessCheckpoint
	err = w.Store.DB.QueryRowContext(ctx, `SELECT version,cursor,ceiling,watermark,phase FROM acl_build WHERE revision=?`, revision).Scan(&cp.Version, &cp.Cursor, &cp.Ceiling, &cp.Watermark, &cp.Phase)
	if err == sql.ErrNoRows || (err == nil && cp.Version != r.Version) {
		if r.Status != "preparing" {
			return false, ErrAccessNotReady
		}
		tx, e := w.Store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return false, e
		}
		ids, e := accessPrincipals(ctx, tx)
		if e != nil {
			tx.Rollback()
			return false, e
		}
		var total int64
		e = tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0),count(*) FROM books`).Scan(&cp.Ceiling, &total)
		if e == nil {
			e = tx.QueryRowContext(ctx, `SELECT source_watermark FROM acl_state WHERE id=1`).Scan(&cp.Watermark)
		}
		if e != nil {
			tx.Rollback()
			return false, e
		}
		if e = tx.Commit(); e != nil {
			return false, e
		}
		principals := make([]string, 0, len(ids))
		for id := range ids {
			principals = append(principals, id)
		}
		if _, e = w.Store.BuildAccessProfiles(ctx, AccessBuildToken{Revision: revision, Version: r.Version, Lease: lease}, principals); e != nil {
			return false, e
		}
		tx, e = w.Store.DB.BeginTx(ctx, nil)
		if e != nil {
			return false, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); e != nil {
			return false, e
		}
		if e = accessBuildCurrent(ctx, tx, AccessBuildToken{Revision: revision, Version: r.Version, Lease: lease}); e != nil {
			return false, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO acl_build(revision,version,cursor,ceiling,watermark,phase) VALUES(?,?,0,?,?,'initial') ON CONFLICT(revision) DO UPDATE SET version=excluded.version,cursor=0,ceiling=excluded.ceiling,watermark=excluded.watermark,phase='initial'`, revision, r.Version, cp.Ceiling, cp.Watermark); e != nil {
			return false, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE acl_revision SET total=?,processed=0 WHERE id=?`, total, revision); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	profiles, err := w.Store.loadAccessProfiles(ctx, r)
	if err != nil {
		return false, err
	}
	tx, err := w.Store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: !w.ReadWrites})
	if err != nil {
		return false, err
	}
	if w.ReadWrites {
		if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
			tx.Rollback()
			return false, err
		}
	}
	var watermark int64
	if err = tx.QueryRowContext(ctx, `SELECT source_watermark FROM acl_state WHERE id=1`).Scan(&watermark); err != nil {
		tx.Rollback()
		return false, err
	}
	query := `SELECT id,id FROM books WHERE id>? AND id<=? ORDER BY id LIMIT ?`
	args := []any{cp.Cursor, cp.Ceiling, limit}
	if cp.Phase != "initial" {
		query = `SELECT book_id,version FROM acl_dirty WHERE version>? AND version<=? ORDER BY version,book_id LIMIT ?`
		args = []any{cp.Watermark, watermark, limit}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		tx.Rollback()
		return false, err
	}
	ids := []int64{}
	cursor := cp.Cursor
	applied := cp.Watermark
	for rows.Next() {
		var id, v int64
		if err = rows.Scan(&id, &v); err != nil {
			rows.Close()
			tx.Rollback()
			return false, err
		}
		ids = append(ids, id)
		if cp.Phase == "initial" {
			cursor = id
		} else {
			applied = v
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		tx.Rollback()
		return false, err
	}
	var books []AccessBookSnapshot
	if len(ids) > 0 {
		books, err = w.Read(ctx, tx, ids)
		if err != nil {
			tx.Rollback()
			return false, err
		}
		if w.ReadWrites {
			if err = tx.QueryRowContext(ctx, `SELECT source_watermark FROM acl_state WHERE id=1`).Scan(&watermark); err != nil {
				tx.Rollback()
				return false, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	if len(ids) == 0 && cp.Phase != "initial" {
		if r.Status == "preparing" {
			err = w.Store.FinishPreparation(ctx, AccessBuildToken{Revision: revision, Version: r.Version, Watermark: watermark, Lease: lease})
			return err == nil, err
		}
		// A ready draft may have caught up after its preview was generated.
		// Refresh the review watermark only if no mutation raced this read.
		if r.Status == "ready" && r.Watermark != watermark {
			res, e := w.Store.DB.ExecContext(ctx, `UPDATE acl_revision SET watermark=?,updated_at=? WHERE id=? AND version=? AND status='ready' AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1) AND ?=(SELECT source_watermark FROM acl_state WHERE id=1) AND EXISTS(SELECT 1 FROM acl_worker_lease WHERE id=1 AND owner=? AND expires>?)`, watermark, time.Now().Unix(), revision, r.Version, watermark, lease, time.Now().UnixMilli())
			if e != nil {
				return false, e
			}
			n, e := res.RowsAffected()
			return n == 1, e
		}
		return false, nil
	}
	if len(ids) > 0 {
		if len(books) != len(ids) {
			return false, fmt.Errorf("incomplete access snapshot")
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		for _, b := range books {
			if !seen[b.ID] {
				return false, fmt.Errorf("unexpected access snapshot")
			}
			delete(seen, b.ID)
		}
		if err = w.Store.WriteAccessBatch(ctx, AccessBuildToken{Revision: revision, Version: r.Version, Watermark: watermark, Lease: lease}, profiles, books); err != nil {
			return false, err
		}
	}
	phase := cp.Phase
	if cp.Phase == "initial" && len(ids) == 0 {
		phase = "delta"
	}
	tx, err = w.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return false, err
	}
	if err = accessProjectionCurrent(ctx, tx, AccessBuildToken{Revision: revision, Version: r.Version, Lease: lease}); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE acl_build SET cursor=?,watermark=?,phase=? WHERE revision=? AND version=?`, cursor, applied, phase, revision, r.Version); err != nil {
		return false, err
	}
	if cp.Phase == "initial" {
		if _, err = tx.ExecContext(ctx, `UPDATE acl_revision SET processed=processed+?,updated_at=? WHERE id=?`, len(ids), time.Now().Unix(), revision); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

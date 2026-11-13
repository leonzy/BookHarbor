package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"talebook/booksvr/internal/access"
)

// AssignAccessClass changes a draft only. The active catalogue keeps using its
// old membership until the new revision is prepared, previewed and published.
func (s AccessStore) AssignAccessClass(ctx context.Context, revision, version int64, class string, books []int64, assigned bool, actor string) (AccessRevision, error) {
	if len(books) == 0 || len(books) > 200 {
		return AccessRevision{}, fmt.Errorf("select 1 to 200 books")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AccessRevision{}, err
	}
	defer tx.Rollback()
	var validActor bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_principal WHERE id=? AND disabled=0)`, actor).Scan(&validActor); err != nil {
		return AccessRevision{}, err
	}
	if !validActor {
		return AccessRevision{}, ErrAccessConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE acl_revision SET version=version+1,status='draft',processed=0,total=0,updated_at=? WHERE id=? AND version=? AND status IN ('draft','failed','cancelled') AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1)`, time.Now().Unix(), revision, version)
	if err != nil {
		return AccessRevision{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return AccessRevision{}, err
	}
	if n != 1 {
		return AccessRevision{}, ErrAccessConflict
	}
	var raw string
	var policy access.Policy
	if err = tx.QueryRowContext(ctx, `SELECT policy FROM acl_revision WHERE id=?`, revision).Scan(&raw); err != nil {
		return AccessRevision{}, err
	}
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		return AccessRevision{}, err
	}
	found := false
	for _, c := range policy.Classes {
		if c.ID == class {
			found = true
			break
		}
	}
	if !found {
		return AccessRevision{}, fmt.Errorf("unknown permission class")
	}
	for _, id := range books {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE id=?)`, id).Scan(&exists); err != nil {
			return AccessRevision{}, err
		}
		if !exists {
			return AccessRevision{}, sql.ErrNoRows
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO acl_class_edit VALUES(?,?,?,?) ON CONFLICT(revision,book_id,class_id) DO UPDATE SET assigned=excluded.assigned`, revision, id, class, assigned); err != nil {
			return AccessRevision{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO acl_audit(revision,actor,action,created_at) VALUES(?,?,'assign_class',?)`, revision, actor, time.Now().Unix()); err != nil {
		return AccessRevision{}, err
	}
	if err = tx.Commit(); err != nil {
		return AccessRevision{}, err
	}
	return s.Revision(ctx, revision)
}

func accessBookClasses(ctx context.Context, tx *sql.Tx, r AccessRevision, id int64) ([]string, error) {
	base := r.BaseRevision
	if r.Status == "active" {
		base = r.ID
	}
	rows, err := tx.QueryContext(ctx, `SELECT class_id FROM acl_class_membership m WHERE revision=? AND book_id=? AND NOT EXISTS(SELECT 1 FROM acl_class_edit e WHERE e.revision=? AND e.book_id=m.book_id AND e.class_id=m.class_id) UNION SELECT class_id FROM acl_class_edit WHERE revision=? AND book_id=? AND assigned=1`, base, id, r.ID, r.ID, id)
	if err != nil {
		return nil, err
	}
	valid := map[string]bool{}
	for _, c := range r.Policy.Classes {
		valid[c.ID] = true
	}
	classes := []string{}
	for rows.Next() {
		var class string
		if err = rows.Scan(&class); err != nil {
			rows.Close()
			return nil, err
		}
		if valid[class] {
			classes = append(classes, class)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM acl_class_membership WHERE revision=? AND book_id=?`, r.ID, id); err != nil {
		return nil, err
	}
	for _, class := range classes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO acl_class_membership VALUES(?,?,?)`, r.ID, id, class); err != nil {
			return nil, err
		}
	}
	return classes, nil
}

func (s AccessStore) AccessClassBooks(ctx context.Context, revision int64, class string, start int) ([]AccessPreviewBook, int, error) {
	if start < 0 {
		return nil, 0, fmt.Errorf("invalid offset")
	}
	r, err := s.Revision(ctx, revision)
	if err != nil {
		return nil, 0, err
	}
	base := r.BaseRevision
	if r.Status == "active" {
		base = r.ID
	}
	query := `SELECT book_id FROM acl_class_membership m WHERE revision=? AND class_id=? AND NOT EXISTS(SELECT 1 FROM acl_class_edit e WHERE e.revision=? AND e.book_id=m.book_id AND e.class_id=m.class_id) UNION SELECT book_id FROM acl_class_edit WHERE revision=? AND class_id=? AND assigned=1`
	args := []any{base, class, revision, revision, class}
	var total int
	if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM books WHERE id IN (`+query+`)`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT b.id,b.title FROM books b WHERE b.id IN (`+query+`) ORDER BY b.id LIMIT 50 OFFSET ?`, append(args, start)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []AccessPreviewBook{}
	for rows.Next() {
		var b AccessPreviewBook
		if err = rows.Scan(&b.ID, &b.Title); err != nil {
			return nil, 0, err
		}
		items = append(items, b)
	}
	return items, total, rows.Err()
}

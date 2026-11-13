package library

import (
	"context"
	"database/sql"
	"fmt"
)

// AccessScope has no exported authorization fields. HTTP input cannot populate
// a profile ID. Only authenticated resolution or an admin-only preview caller
// can obtain a scope; a zero value is deliberately invalid.
type AccessScope struct {
	revision     int64
	profile      string
	principal    string
	epoch        int64
	profileEpoch int64
	valid        bool
	unrestricted bool
	preview      bool
}

func (s AccessScope) Unrestricted() bool { return s.valid && s.unrestricted }
func (s AccessScope) Revision() int64    { return s.revision }

// AccessCanViewBook is only a metadata gate. Reading a format still requires
// the exact immutable file identity and source version in AccessCanReadFile.
func (s AccessStore) AccessCanViewBook(ctx context.Context, scope AccessScope, bookID int64) (bool, error) {
	if !scope.valid || scope.profile == "" {
		return false, ErrAccessNotReady
	}
	var allowed bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_profile_book WHERE revision=? AND profile=? AND book_id=? AND owner IN ('',?))`, scope.revision, scope.profile, bookID, scope.principal).Scan(&allowed)
	if err != nil {
		return false, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return false, err
	}
	return allowed, nil
}

func (s AccessStore) ResolveAccess(ctx context.Context, accountID int64, admin bool) (AccessScope, error) {
	var out AccessScope
	var disabled int
	err := s.DB.QueryRowContext(ctx, `SELECT s.active_revision,s.policy_epoch,p.id,p.disabled FROM acl_state s JOIN acl_principal p ON p.account_id=? WHERE s.id=1`, accountID).Scan(&out.revision, &out.epoch, &out.principal, &disabled)
	if err != nil {
		return out, err
	}
	if disabled != 0 {
		return out, ErrAccessNotReady
	}
	if admin || out.revision == 0 {
		out.valid = true
		out.unrestricted = true
		return out, nil
	}
	var unrestricted bool
	err = s.DB.QueryRowContext(ctx, `SELECT m.profile,p.unrestricted,p.epoch FROM acl_profile_principal m JOIN acl_profile p ON p.revision=m.revision AND p.profile=m.profile WHERE m.revision=? AND m.principal=?`, out.revision, out.principal).Scan(&out.profile, &unrestricted, &out.profileEpoch)
	if err != nil {
		return AccessScope{}, ErrAccessNotReady
	}
	// Even a resource-unrestricted projection still has private ownership.
	// The HTTP integration may select the legacy private-aware fast path only
	// after checking that all legacy readers understand the active generation.
	out.valid = true
	// Source health, ownership and inherited restrictions are part of every
	// active projection, even when its policy has no directory ceiling.
	out.unrestricted = false
	return out, nil
}

func (s AccessStore) PreviewAccess(ctx context.Context, revision int64, principal string) (AccessScope, error) {
	var out AccessScope
	out.revision = revision
	out.principal = principal
	out.preview = true
	err := s.DB.QueryRowContext(ctx, `SELECT m.profile,r.version,p.epoch FROM acl_profile_principal m JOIN acl_revision r ON r.id=m.revision JOIN acl_principal u ON u.id=m.principal JOIN acl_profile p ON p.revision=m.revision AND p.profile=m.profile WHERE m.revision=? AND m.principal=? AND r.status='ready' AND r.watermark=(SELECT source_watermark FROM acl_state WHERE id=1) AND u.disabled=0`, revision, principal).Scan(&out.profile, &out.epoch, &out.profileEpoch)
	if err != nil {
		return out, ErrAccessNotReady
	}
	out.valid = true
	// Always use the actual projected scope for a preview; never admin bypass.
	return out, nil
}

func (s AccessStore) AccessCurrent(ctx context.Context, scope AccessScope) error {
	if !scope.valid {
		return ErrAccessNotReady
	}
	var n int
	var query string
	var args []any
	if scope.preview {
		query = `SELECT count(*) FROM acl_revision r JOIN acl_principal p ON p.id=? WHERE r.id=? AND r.version=? AND r.status='ready' AND r.watermark=(SELECT source_watermark FROM acl_state WHERE id=1) AND p.disabled=0`
		args = []any{scope.principal, scope.revision, scope.epoch}
	} else {
		query = `SELECT count(*) FROM acl_state s JOIN acl_principal p ON p.id=? WHERE s.id=1 AND s.active_revision=? AND s.policy_epoch=? AND p.disabled=0`
		args = []any{scope.principal, scope.revision, scope.epoch}
	}
	if scope.profile != "" {
		query += ` AND EXISTS(SELECT 1 FROM acl_profile WHERE revision=? AND profile=? AND epoch=?)`
		args = append(args, scope.revision, scope.profile, scope.profileEpoch)
		// Offline roots revoke immediately; the bounded root replay removes
		// their grants and counters before this profile can be read again.
		query += ` AND NOT EXISTS(SELECT 1 FROM acl_root r WHERE r.ready=0 AND EXISTS(SELECT 1 FROM acl_profile_file f WHERE f.revision=? AND f.profile=? AND f.source_root=r.id))`
		args = append(args, scope.revision, scope.profile)
	}
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrAccessConflict
	}
	return nil
}

// AccessCanReadFile is a primary-key lookup on a specific immutable file ID.
// Fast-path callers still have to perform their existing private/source checks.
func (s AccessStore) AccessCanReadFile(ctx context.Context, scope AccessScope, fileID string, sourceVersion int64) (bool, error) {
	if !scope.valid {
		return false, ErrAccessNotReady
	}
	if scope.unrestricted {
		return false, fmt.Errorf("unrestricted scope must use the private-aware resource selector")
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM acl_profile_file WHERE revision=? AND profile=? AND file_id=? AND source_version=? AND owner IN ('',?)`, scope.revision, scope.profile, fileID, sourceVersion, scope.principal).Scan(&n)
	if err != nil {
		return false, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return false, err
	}
	return n == 1, nil
}

type AccessPreviewBook struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Size  int64  `json:"size"`
}

// AccessPreviewPage serves administrator impact previews, not grouped catalog
// cards. Its count is explicitly edition records; full browsing must fold works
// only after applying the same file/owner filter.
func (s AccessStore) AccessPreviewPage(ctx context.Context, scope AccessScope, start, size int, order string) ([]AccessPreviewBook, int64, error) {
	if !scope.valid || !scope.preview || start < 0 || size < 1 || size > 100 {
		return nil, 0, ErrAccessNotReady
	}
	sortSQL := "book_id DESC"
	switch order {
	case "title":
		sortSQL = "title,book_id DESC"
	case "size":
		sortSQL = "size DESC,book_id DESC"
	case "newest":
		sortSQL = "timestamp DESC,book_id DESC"
	default:
		return nil, 0, fmt.Errorf("invalid sort")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(books),0) FROM acl_profile_count WHERE revision=? AND profile=? AND owner IN ('',?)`, scope.revision, scope.profile, scope.principal).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT book_id,title,size FROM acl_profile_book WHERE revision=? AND profile=? AND owner IN ('',?) ORDER BY `+sortSQL+` LIMIT ? OFFSET ?`, scope.revision, scope.profile, scope.principal, size, start)
	if err != nil {
		return nil, 0, err
	}
	books := []AccessPreviewBook{}
	for rows.Next() {
		var b AccessPreviewBook
		if err = rows.Scan(&b.ID, &b.Title, &b.Size); err != nil {
			rows.Close()
			return nil, 0, err
		}
		books = append(books, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return nil, 0, err
	}
	return books, total, nil
}

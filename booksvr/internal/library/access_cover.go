package library

import "context"

// Legacy artwork has no reliable source-format provenance. It may be shared
// only when every source format is authorized; otherwise callers use a neutral
// placeholder instead of revealing an image extracted from a hidden source.
func (s AccessStore) AccessCanShareCover(ctx context.Context, scope AccessScope, bookID int64) (bool, error) {
	if !scope.valid || scope.profile == "" {
		return false, ErrAccessNotReady
	}
	var allowed bool
	err := s.DB.QueryRowContext(ctx, `WITH sources AS (
SELECT 'f:'||id source_key FROM book_formats WHERE book_id=?
UNION ALL SELECT 'd:'||d.rowid FROM data d WHERE d.book=? AND NOT EXISTS(SELECT 1 FROM book_formats f WHERE f.book_id=d.book AND upper(f.format)=upper(d.format)))
SELECT EXISTS(SELECT 1 FROM sources) AND NOT EXISTS(SELECT 1 FROM sources s LEFT JOIN acl_source_identity i ON i.source_key=s.source_key WHERE i.id IS NULL OR NOT EXISTS(SELECT 1 FROM acl_profile_file f WHERE f.revision=? AND f.profile=? AND f.file_id=i.id AND f.source_version=i.version AND f.owner IN ('',?)))`, bookID, bookID, scope.revision, scope.profile, scope.principal).Scan(&allowed)
	if err != nil {
		return false, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return false, err
	}
	return allowed, nil
}

package library

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"talebook/booksvr/internal/sourcefile"
)

type AccessFile struct {
	ID      string
	Version int64
	Path    string
	Format  string
	Size    int64
}

// AccessResolveFile selects and authorizes the same current physical resource.
// Native formats override legacy data rows exactly as in snapshot construction.
// No fallback may be attempted by callers after this returns not-found.
func (s AccessStore) AccessResolveFile(ctx context.Context, scope AccessScope, bookID int64, format, libraryRoot string) (AccessFile, error) {
	if !scope.valid || scope.profile == "" || scope.preview {
		return AccessFile{}, ErrAccessNotReady
	}
	format = strings.ToUpper(strings.TrimSpace(format))
	if format == "" {
		return AccessFile{}, fmt.Errorf("missing format")
	}
	query := `WITH candidate AS (SELECT 'f:'||f.id source_key,f.path,f.size,upper(f.format) format FROM book_formats f WHERE f.book_id=? AND upper(f.format)=? AND NOT EXISTS(SELECT 1 FROM source_sync_formats ss WHERE ss.format_id=f.id AND ss.missing=1) UNION ALL SELECT 'd:'||d.rowid,b.path||'/'||d.name||'.'||lower(d.format),coalesce(d.uncompressed_size,0),upper(d.format) FROM data d JOIN books b ON b.id=d.book WHERE d.book=? AND upper(d.format)=? AND NOT EXISTS(SELECT 1 FROM book_formats f WHERE f.book_id=d.book AND upper(f.format)=upper(d.format))) SELECT i.id,i.version,c.path,c.format,c.size,r.path,r.token FROM candidate c JOIN acl_source_identity i ON i.source_key=c.source_key JOIN acl_root r ON r.id=i.root AND r.ready=1 JOIN acl_profile_file p ON p.file_id=i.id AND p.source_version=i.version AND p.book_id=? AND p.revision=? AND p.profile=? AND p.owner IN ('',?) LIMIT 2`
	rows, err := s.DB.QueryContext(ctx, query, bookID, format, bookID, format, bookID, scope.revision, scope.profile, scope.principal)
	if err != nil {
		return AccessFile{}, err
	}
	var out AccessFile
	var root, token string
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&out.ID, &out.Version, &out.Path, &out.Format, &out.Size, &root, &token); err != nil {
			rows.Close()
			return AccessFile{}, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return AccessFile{}, err
	}
	if count != 1 {
		return AccessFile{}, sql.ErrNoRows
	}
	if !accessRootHealthy(root, token) {
		return AccessFile{}, ErrAccessNotReady
	}
	if !sourcefile.IsReference(out.Path) && !filepath.IsAbs(out.Path) {
		out.Path = filepath.Join(libraryRoot, out.Path)
	}
	physical := out.Path
	if sourcefile.IsReference(physical) {
		ref, e := sourcefile.Decode(physical)
		if e != nil {
			return AccessFile{}, e
		}
		physical = ref.Archive
	}
	if err = accessCheckPhysicalPath(root, physical); err != nil {
		return AccessFile{}, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return AccessFile{}, err
	}
	return out, nil
}

// Do not follow a symlink inside an allowed directory to a restricted source.
// The configured root itself may be a mount alias, but descendants must retain
// their indexed location. Missing components remain the normal file error.
func accessCheckPhysicalPath(root, physical string) error {
	rel, err := filepath.Rel(root, physical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return sql.ErrNoRows
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func accessRootHealthy(root, token string) bool {
	if token == "" {
		return false
	}
	f, err := os.Open(filepath.Join(root, ".talebook-source-health"))
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	return err == nil && len(data) <= 4096 && string(data) == token
}

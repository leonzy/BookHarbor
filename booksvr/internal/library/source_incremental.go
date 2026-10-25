package library

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/sourcefile"
)

// CheckSourceRoots performs bounded, fail-closed checks independent of library size.
func CheckSourceRoots(ctx context.Context, db *sql.DB, roots []string) (map[string]string, error) {
	out := map[string]string{}
	for _, root := range normalizeSourceRoots(roots) {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			out[root] = "源目录不可访问，暂停删除同步"
			continue
		}
		if strings.HasPrefix(sourceRootIdentity(root, info), "unverified:") {
			out[root] = "无法确认文件系统身份，暂停删除同步"
			continue
		}
		var identity, token string
		err = db.QueryRowContext(ctx, `SELECT identity,token FROM source_sync_roots WHERE root=?`, root).Scan(&identity, &token)
		if err == sql.ErrNoRows {
			out[root] = "uninitialized"
			continue
		}
		if err != nil {
			return nil, err
		}
		if token != "" {
			b, e := os.ReadFile(filepath.Join(root, ".talebook-source-health"))
			if e != nil || string(b) != token {
				out[root] = "源目录健康标记不可用，暂停删除同步"
				continue
			}
		}
		if identity != "" && identity != sourceRootIdentity(root, info) {
			out[root] = "源目录身份变化，暂停删除同步"
			continue
		}
		out[root] = "ok"
	}
	return out, nil
}

func compactSourcePaths(paths []string) []string {
	out := normalizeSourceRoots(paths)
	return out
}

// The small trigger queue includes writes made by imports while synchronization
// is running. Each transaction removes exactly the rows whose paths it indexed.
func refreshSourcePaths(ctx context.Context, db *sql.DB) error {
	for {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_sync_pending SET format_id=format_id WHERE 0`); err != nil {
			tx.Rollback()
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT f.id,f.path FROM source_sync_pending p JOIN book_formats f ON f.id=p.format_id ORDER BY p.format_id LIMIT 500`)
		if err != nil {
			tx.Rollback()
			return err
		}
		type entry struct {
			id   int64
			path string
		}
		var batch []entry
		for rows.Next() {
			var e entry
			if err = rows.Scan(&e.id, &e.path); err != nil {
				break
			}
			batch = append(batch, e)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		put, err := tx.PrepareContext(ctx, `INSERT INTO source_sync_paths VALUES(?,?) ON CONFLICT(format_id) DO UPDATE SET physical_path=excluded.physical_path`)
		if err != nil {
			tx.Rollback()
			return err
		}
		del, err := tx.PrepareContext(ctx, `DELETE FROM source_sync_pending WHERE format_id=?`)
		if err != nil {
			put.Close()
			tx.Rollback()
			return err
		}
		for _, e := range batch {
			if err != nil {
				break
			}
			path := e.path
			if sourcefile.IsReference(path) {
				ref, e := sourcefile.Decode(path)
				if e == nil {
					path = ref.Archive
				}
			}
			_, err = put.ExecContext(ctx, e.id, path)
			if err == nil {
				_, err = del.ExecContext(ctx, e.id)
			}
		}
		put.Close()
		del.Close()
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if len(batch) < 500 {
			return nil
		}
	}
}

const sourceTrackedSelect = `SELECT f.id,f.book_id,f.format,f.path,COALESCE(f.hash,''),COALESCE(s.identity,''),COALESCE(s.size,f.size,0),COALESCE(s.mtime,0),COALESCE(s.missing,0) FROM book_formats f LEFT JOIN source_sync_formats s ON s.format_id=f.id`

func loadSourceTracked(ctx context.Context, db *sql.DB, option SourceSyncOptions, libraryRoot string) ([]sourceTracked, error) {
	var out []sourceTracked
	seen := map[int64]bool{}
	queries := []string{sourceTrackedSelect}
	args := [][]any{nil}
	if option.Incremental {
		queries = nil
		args = nil
		for _, p := range compactSourcePaths(option.ChangedPaths) {
			prefix := strings.TrimRight(p, string(filepath.Separator)) + string(filepath.Separator)
			queries = append(queries, sourceTrackedSelect+` WHERE f.id IN (SELECT format_id FROM source_sync_paths WHERE physical_path=? UNION ALL SELECT format_id FROM source_sync_paths WHERE physical_path>=? AND physical_path<? UNION SELECT format_id FROM source_sync_formats WHERE missing=1 AND size IN (SELECT size FROM source_sync_candidates WHERE path=? UNION ALL SELECT size FROM source_sync_candidates WHERE path>=? AND path<?))`)
			args = append(args, []any{p, prefix, prefix[:len(prefix)-1] + string(filepath.Separator+1), p, prefix, prefix[:len(prefix)-1] + string(filepath.Separator+1)})
		}
	}
	for i, q := range queries {
		rows, err := db.QueryContext(ctx, q, args[i]...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t sourceTracked
			if err = rows.Scan(&t.id, &t.book, &t.format, &t.path, &t.hash, &t.key, &t.size, &t.mtime, &t.missing); err != nil {
				break
			}
			if seen[t.id] {
				continue
			}
			seen[t.id] = true
			t.physical = t.path
			if sourcefile.IsReference(t.path) {
				r, e := sourcefile.Decode(t.path)
				if e != nil {
					continue
				}
				t.ref = &r
				t.physical = r.Archive
			}
			if filepath.IsAbs(t.physical) && !(libraryRoot != "" && sourceWithin(t.physical, libraryRoot)) {
				out = append(out, t)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Root filesystem identity is checked before and after synchronization. Device
// numbering can change at boot while the inode on that same filesystem remains.
func sameSourceInode(a, b string) bool {
	if a == b {
		return true
	}
	i, j := strings.LastIndex(a, ":"), strings.LastIndex(b, ":")
	return i >= 0 && j >= 0 && a[i+1:] == b[j+1:]
}

// Match split move events by indexed identities, and cross-filesystem restores
// by the size of previously missing sources. Never load every missing record.
func relocationSources(ctx context.Context, db *sql.DB, files map[string]sourceDiskFile, tracked []sourceTracked, libraryRoot string) ([]sourceTracked, error) {
	seen := map[int64]bool{}
	for _, t := range tracked {
		seen[t.id] = true
	}
	keys := map[string]bool{}
	sizes := map[int64]bool{}
	var out []sourceTracked
	for _, f := range files {
		var predicates []string
		var args []any
		if !keys[f.key] {
			predicates = append(predicates, `SELECT format_id FROM source_sync_formats WHERE identity=?`)
			args = append(args, f.key)
			keys[f.key] = true
		}
		if !sizes[f.size] {
			predicates = append(predicates, `SELECT format_id FROM source_sync_formats WHERE missing=1 AND size=?`)
			args = append(args, f.size)
			sizes[f.size] = true
		}
		if len(predicates) == 0 {
			continue
		}
		rows, err := db.QueryContext(ctx, sourceTrackedSelect+` WHERE f.id IN (`+strings.Join(predicates, ` UNION `)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t sourceTracked
			if err = rows.Scan(&t.id, &t.book, &t.format, &t.path, &t.hash, &t.key, &t.size, &t.mtime, &t.missing); err != nil {
				break
			}
			if seen[t.id] {
				continue
			}
			seen[t.id] = true
			t.physical = t.path
			if sourcefile.IsReference(t.path) {
				r, e := sourcefile.Decode(t.path)
				if e != nil {
					continue
				}
				t.ref = &r
				t.physical = r.Archive
			}
			if !filepath.IsAbs(t.physical) || libraryRoot != "" && sourceWithin(t.physical, libraryRoot) {
				continue
			}
			if _, exists := files[t.physical]; exists {
				continue
			}
			if _, e := os.Stat(t.physical); !os.IsNotExist(e) || sourcePathHasSymlink(t.physical) {
				continue
			}
			out = append(out, t)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

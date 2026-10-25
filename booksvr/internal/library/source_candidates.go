package library

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Only path and byte count are persisted. Ebook bodies are never cached here.
// This bridges copy-then-unlink moves whose events arrive in different batches,
// independently of the automatic NEW-book import switch.
func sourceStoredCandidates(ctx context.Context, db *sql.DB, tracked []sourceTracked, files map[string]sourceDiskFile, healthy func(string) bool) ([]sourceDiskFile, error) {
	sizes := map[int64]bool{}
	seen := map[string]bool{}
	var out []sourceDiskFile
	for _, t := range tracked {
		if _, ok := files[t.physical]; ok || !healthy(t.physical) || sizes[t.size] || !strings.HasPrefix(t.hash, "sha256:") {
			continue
		}
		sizes[t.size] = true
		rows, err := db.QueryContext(ctx, `SELECT c.path FROM source_sync_candidates c WHERE c.size=? AND NOT EXISTS(SELECT 1 FROM source_sync_paths p WHERE p.physical_path=c.path)`, t.size)
		if err != nil {
			return nil, err
		}
		var paths []string
		for rows.Next() {
			var p string
			if err = rows.Scan(&p); err != nil {
				break
			}
			paths = append(paths, p)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			if seen[p] || !healthy(p) {
				continue
			}
			seen[p] = true
			if _, ok := files[p]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, e := os.Stat(p)
			if e == nil && info.Mode().IsRegular() && info.Size() == t.size {
				out = append(out, sourceDiskFile{p, sourceFileIdentity(info), info.Size(), info.ModTime().UnixNano()})
			}
		}
	}
	return out, nil
}

func saveSourceCandidates(ctx context.Context, db *sql.DB, files map[string]sourceDiskFile, claimed map[string]string, option SourceSyncOptions, healthy func(string) bool) error {
	old := map[string]int64{}
	queries := []string{`SELECT path,size FROM source_sync_candidates`}
	args := [][]any{nil}
	if option.Incremental {
		queries = nil
		args = nil
		for _, p := range compactSourcePaths(option.ChangedPaths) {
			prefix := strings.TrimRight(p, string(filepath.Separator)) + string(filepath.Separator)
			queries = append(queries, `SELECT path,size FROM source_sync_candidates WHERE path=? OR (path>=? AND path<?)`)
			args = append(args, []any{p, prefix, prefix[:len(prefix)-1] + string(filepath.Separator+1)})
		}
	}
	for i, q := range queries {
		rows, e := db.QueryContext(ctx, q, args[i]...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var p string
			var size int64
			if e = rows.Scan(&p, &size); e != nil {
				break
			}
			old[p] = size
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
	}
	type edit struct {
		path   string
		size   int64
		remove bool
	}
	var batch []edit
	flush := func() error {
		if len(batch) == 0 {
			return ctx.Err()
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		put, e := tx.PrepareContext(ctx, `INSERT INTO source_sync_candidates VALUES(?,?) ON CONFLICT(path) DO UPDATE SET size=excluded.size WHERE source_sync_candidates.size<>excluded.size`)
		if e != nil {
			return e
		}
		defer put.Close()
		del, e := tx.PrepareContext(ctx, `DELETE FROM source_sync_candidates WHERE path=?`)
		if e != nil {
			return e
		}
		defer del.Close()
		for _, v := range batch {
			if v.remove {
				_, e = del.ExecContext(ctx, v.path)
			} else {
				_, e = put.ExecContext(ctx, v.path, v.size)
			}
			if e != nil {
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		batch = batch[:0]
		// Give the active import writer a chance between bounded metadata batches.
		time.Sleep(time.Millisecond)
		return ctx.Err()
	}
	add := func(v edit) error {
		batch = append(batch, v)
		if len(batch) >= 500 {
			return flush()
		}
		return nil
	}
	var paths []string
	for p := range old {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !healthy(p) {
			continue
		}
		_, exists := files[p]
		if !exists || claimed[p] != "" {
			if e := add(edit{path: p, remove: true}); e != nil {
				return e
			}
		}
	}
	paths = nil
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !healthy(p) {
			continue
		}
		f := files[p]
		if claimed[p] != "" {
			if _, inScope := old[p]; !inScope {
				if e := add(edit{path: p, remove: true}); e != nil {
					return e
				}
			}
			continue
		}
		if size, ok := old[p]; ok && size == f.size {
			continue
		}
		if e := add(edit{path: p, size: f.size}); e != nil {
			return e
		}
	}
	return flush()
}

// A partial event snapshot can be empty while a read-only root still contains
// books elsewhere. Probe a bounded number of indexed physical paths before
// applying the existing empty-root quarantine; never walk the entire root.
func sourceRootHasLiveFile(ctx context.Context, db *sql.DB, root string) bool {
	prefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
	upper := prefix[:len(prefix)-1] + string(filepath.Separator+1)
	for _, q := range []string{`SELECT DISTINCT physical_path FROM source_sync_paths WHERE physical_path>=? AND physical_path<? ORDER BY physical_path LIMIT 32`, `SELECT path FROM source_sync_candidates WHERE path>=? AND path<? ORDER BY path LIMIT 32`} {
		rows, e := db.QueryContext(ctx, q, prefix, upper)
		if e != nil {
			return false
		}
		var paths []string
		for rows.Next() {
			var p string
			if e = rows.Scan(&p); e != nil {
				break
			}
			paths = append(paths, p)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return false
		}
		for _, p := range paths {
			if info, e := os.Stat(p); e == nil && info.Mode().IsRegular() && !sourcePathHasSymlink(p) {
				return true
			}
		}
	}
	return false
}

package scan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/userdb"
	"time"
)

type activeDirectoryTask struct {
	id    int64
	scope string
}

// Return bounded work fairly for each healthy root. Files in the same parent
// share a task; their persisted events still force in-place file rechecks.
func (s *Service) DirectoryDirtyPaths(ctx context.Context, roots []string, limit int) ([]string, error) {
	if limit < 1 || limit > 500 {
		limit = 256
	}
	set := map[string]bool{}
	for _, root := range roots {
		if s.CheckDirectoryRoot != nil {
			if e := s.CheckDirectoryRoot(ctx, root); e != nil {
				continue
			}
		}
		base := strings.TrimRight(root, string(filepath.Separator))
		prefix := base + string(filepath.Separator)
		rows, e := s.DB.Query(`SELECT path,version FROM scan_directory_events WHERE path=? OR (path>=? AND path<?) ORDER BY version,path LIMIT ?`, root, prefix, base+string(filepath.Separator+1), limit)
		if e != nil {
			return nil, e
		}
		type event struct {
			path    string
			version int64
		}
		var events []event
		for rows.Next() {
			var ev event
			if e = rows.Scan(&ev.path, &ev.version); e != nil {
				rows.Close()
				return nil, e
			}
			events = append(events, ev)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, ev := range events {
			info, e := os.Stat(ev.path)
			if os.IsNotExist(e) {
				if _, e = s.DB.Exec(`DELETE FROM scan_directory_events WHERE path=? AND version=?`, ev.path, ev.version); e != nil {
					return nil, e
				}
				continue
			}
			p := ev.path
			if e == nil && !info.IsDir() {
				ext := strings.ToLower(filepath.Ext(p))
				if ext != ".zip" && ext != ".rar" {
					p = filepath.Dir(p)
				}
			}
			set[p] = true
		}
	}
	var paths []string
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func SourceWithin(row *userdb.ScanFile, root string) bool {
	if importdir.IsWithin(row.Path, root) {
		return true
	}
	archive, _ := row.Data["archive_path"].(string)
	return archive != "" && importdir.IsWithin(archive, root)
}

// Events survive a paused importer and process restarts. A version is only
// acknowledged by the task which actually enrolled and processed that event.
func (s *Service) RememberDirectoryChanges(paths, fullRoots []string) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	version := time.Now().UnixNano()
	for _, p := range paths {
		if _, e = tx.Exec(`INSERT INTO scan_directory_events(path,version) VALUES(?,?) ON CONFLICT(path) DO UPDATE SET version=excluded.version`, filepath.Clean(p), version); e != nil {
			return e
		}
	}
	for _, p := range fullRoots {
		if _, e = tx.Exec(`INSERT INTO scan_directory_events(path,version) VALUES(?,?) ON CONFLICT(path) DO UPDATE SET version=excluded.version`, ":full:"+filepath.Clean(p), version); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func enrollDirectoryEvents(tx *sql.Tx, id int64, root string, full bool) error {
	base := strings.TrimRight(root, string(filepath.Separator))
	prefix := base + string(filepath.Separator)
	end := base + string(filepath.Separator+1)
	_, e := tx.Exec(`INSERT INTO scan_directory_queue(job_id,path)
	 SELECT ?,path FROM scan_directory_events WHERE path=? OR (path>=? AND path<?)
	 ON CONFLICT(job_id,path) DO UPDATE SET state='pending',cursor='',stamp='' WHERE scan_directory_queue.state='done'`, id, root, prefix, end)
	if e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT OR IGNORE INTO scan_directory_run_events(job_id,path,version) SELECT ?,path,version FROM scan_directory_events WHERE path=? OR (path>=? AND path<?)`, id, root, prefix, end)
	if e != nil {
		return e
	}
	if full {
		_, e = tx.Exec(`INSERT OR IGNORE INTO scan_directory_run_events(job_id,path,version) SELECT ?,path,version FROM scan_directory_events WHERE path=?`, id, ":full:"+root)
	}
	return e
}

func ackDirectoryEvent(tx *sql.Tx, id int64, path string) error {
	_, e := tx.Exec(`DELETE FROM scan_directory_events WHERE path=? AND version=(SELECT version FROM scan_directory_run_events WHERE job_id=? AND path=?)`, path, id, path)
	return e
}

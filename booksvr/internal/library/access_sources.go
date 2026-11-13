package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"talebook/booksvr/internal/access"
	"talebook/booksvr/internal/sourcefile"
)

// Stable root IDs survive device-number changes. A changed mount token never
// silently becomes the same authority; an unavailable root remains registered.
func migrateAccessSources(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS acl_root(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,token TEXT NOT NULL,ready INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS acl_root_pending(root TEXT PRIMARY KEY,cursor TEXT NOT NULL DEFAULT '')`,
		`DROP TRIGGER IF EXISTS acl_root_health`,
		`CREATE TRIGGER acl_root_health AFTER UPDATE OF ready ON acl_root WHEN OLD.ready IS NOT NEW.ready BEGIN UPDATE acl_state SET policy_epoch=policy_epoch+1 WHERE id=1; INSERT INTO acl_root_pending(root,cursor) VALUES(NEW.id,'') ON CONFLICT(root) DO UPDATE SET cursor=''; END`,
		`CREATE TABLE IF NOT EXISTS acl_source_identity(source_key TEXT PRIMARY KEY,id TEXT NOT NULL UNIQUE,version INTEGER NOT NULL DEFAULT 1,root TEXT NOT NULL DEFAULT '',directory TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS acl_source_root_cursor ON acl_source_identity(root,id,source_key)`,
		`CREATE TABLE IF NOT EXISTS acl_source_origin(file_id TEXT NOT NULL,rule_id TEXT NOT NULL,PRIMARY KEY(file_id,rule_id)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_format_lineage(book_id INTEGER NOT NULL,format TEXT NOT NULL,facts TEXT NOT NULL,PRIMARY KEY(book_id,format)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_source_rule(file_id TEXT NOT NULL,revision INTEGER NOT NULL,rule_id TEXT NOT NULL,PRIMARY KEY(file_id,revision,rule_id)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_source_class(file_id TEXT NOT NULL,class_id TEXT NOT NULL,PRIMARY KEY(file_id,class_id)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_book_owner(book_id INTEGER PRIMARY KEY,principal TEXT NOT NULL,private INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS acl_user_sync(id INTEGER PRIMARY KEY CHECK(id=1),watermark INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO acl_user_sync(id) VALUES(1)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for _, s := range []struct{ table, key, columns string }{{"book_formats", "f:", "book_id,format,path,size,mtime,hash"}, {"data", "d:", "book,format,name,uncompressed_size"}} {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, s.table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		if s.table == "book_formats" {
			for _, q := range []string{
				`CREATE TRIGGER IF NOT EXISTS acl_lineage_move AFTER UPDATE OF book_id,format ON book_formats WHEN OLD.book_id IS NOT NEW.book_id OR OLD.format IS NOT NEW.format BEGIN UPDATE acl_format_lineage SET book_id=NEW.book_id,format=upper(NEW.format) WHERE book_id=OLD.book_id AND format=upper(OLD.format); END`,
				`CREATE TRIGGER IF NOT EXISTS acl_lineage_remove AFTER DELETE ON book_formats BEGIN DELETE FROM acl_format_lineage WHERE book_id=OLD.book_id AND format=upper(OLD.format); END`,
			} {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
		}
		key := "'" + s.key + "'||OLD.rowid"
		conditions := []string{}
		for _, column := range strings.Split(s.columns, ",") {
			conditions = append(conditions, "OLD."+column+" IS NOT NEW."+column)
		}
		capture := `INSERT INTO acl_source_origin SELECT i.id,r.rule_id FROM acl_source_identity i JOIN acl_source_rule r ON r.file_id=i.id AND r.revision=(SELECT active_revision FROM acl_state WHERE id=1) WHERE i.source_key=` + key + ` AND NOT EXISTS(SELECT 1 FROM acl_source_origin o WHERE o.file_id=i.id AND o.rule_id=r.rule_id);`
		for _, q := range []string{
			`CREATE TRIGGER IF NOT EXISTS acl_source_` + s.table + `_insert AFTER INSERT ON ` + s.table + ` BEGIN INSERT INTO acl_source_identity(source_key,id) SELECT '` + s.key + `'||NEW.rowid,lower(hex(randomblob(16))) WHERE NOT EXISTS(SELECT 1 FROM acl_source_identity WHERE source_key='` + s.key + `'||NEW.rowid); END`,
			`CREATE TRIGGER IF NOT EXISTS acl_source_` + s.table + `_update AFTER UPDATE OF ` + s.columns + ` ON ` + s.table + ` WHEN ` + strings.Join(conditions, " OR ") + ` BEGIN ` + capture + ` UPDATE acl_source_identity SET version=version+1 WHERE source_key=` + key + `; END`,
			`CREATE TRIGGER IF NOT EXISTS acl_source_` + s.table + `_delete AFTER DELETE ON ` + s.table + ` BEGIN DELETE FROM acl_source_rule WHERE file_id=(SELECT id FROM acl_source_identity WHERE source_key=` + key + `); DELETE FROM acl_source_identity WHERE source_key=` + key + `; END`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	return nil
}

type AccessRoot struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Ready bool   `json:"ready"`
}

// SyncAccessRoots inspects only configured root sentinels, never their books.
// Imported roots reuse source-sync tokens; the managed library gets its own
// exclusive sentinel on first initialization while its database is open.
func (s AccessStore) SyncAccessRoots(ctx context.Context, managed string) error {
	type rootToken struct{ path, token string }
	list := []rootToken{}
	rows, err := s.DB.QueryContext(ctx, `SELECT root,token FROM source_sync_roots WHERE token<>''`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var r rootToken
		if err = rows.Scan(&r.path, &r.token); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
		seen[filepath.Clean(r.path)] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if managed != "" && !seen[filepath.Clean(managed)] {
		managed = filepath.Clean(managed)
		var token string
		err = s.DB.QueryRowContext(ctx, `SELECT token FROM acl_root WHERE path=?`, managed).Scan(&token)
		if err == sql.ErrNoRows {
			path := filepath.Join(managed, ".talebook-source-health")
			var data []byte
			f, e := os.Open(path)
			if e == nil {
				data, e = io.ReadAll(io.LimitReader(f, 4097))
				f.Close()
			}
			if e == nil && len(data) > 0 && len(data) <= 4096 {
				token = string(data)
			} else if os.IsNotExist(e) {
				token, e = accessID()
				if e != nil {
					return e
				}
				f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if e != nil {
					return e
				}
				_, e = f.WriteString(token)
				if e == nil {
					e = f.Sync()
				}
				closeErr := f.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					return closeErr
				}
			} else {
				return fmt.Errorf("managed source sentinel unavailable")
			}
		} else if err != nil {
			return err
		}
		list = append(list, rootToken{managed, token})
	}
	for _, r := range list {
		if _, err = s.ObserveAccessRoot(ctx, r.path, r.token, accessRootHealthy(r.path, r.token)); err != nil {
			return err
		}
	}
	return nil
}

func (s AccessStore) ObserveAccessRoot(ctx context.Context, path, token string, healthy bool) (AccessRoot, error) {
	if !filepath.IsAbs(path) || token == "" {
		return AccessRoot{}, fmt.Errorf("invalid source root identity")
	}
	path = filepath.Clean(path)
	id, err := accessID()
	if err != nil {
		return AccessRoot{}, err
	}
	// The token is immutable. Replacing a mount requires explicit reconciliation.
	_, err = s.DB.ExecContext(ctx, `INSERT INTO acl_root(id,path,token,ready) VALUES(?,?,?,?) ON CONFLICT(path) DO UPDATE SET ready=(acl_root.token=excluded.token AND excluded.ready) WHERE acl_root.ready IS NOT (acl_root.token=excluded.token AND excluded.ready)`, id, path, token, healthy)
	if err != nil {
		return AccessRoot{}, err
	}
	var out AccessRoot
	err = s.DB.QueryRowContext(ctx, `SELECT id,path,ready FROM acl_root WHERE path=?`, path).Scan(&out.ID, &out.Path, &out.Ready)
	return out, err
}

// AccessSourceReader returns the authoritative file inventory without touching
// the filesystem. Root health is supplied by the existing mount monitor.
// Use it with AccessWorker.ReadWrites: identity backfill is bounded to this batch.
func AccessSourceReader(libraryRoot string) AccessSnapshotReader {
	return func(ctx context.Context, tx *sql.Tx, ids []int64) ([]AccessBookSnapshot, error) {
		if len(ids) > 64 {
			return nil, fmt.Errorf("source batch exceeds budget")
		}
		param := jsonIDs(ids)
		roots := []AccessRoot{}
		rows, err := tx.QueryContext(ctx, `SELECT id,path,ready FROM acl_root ORDER BY length(path) DESC,path`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r AccessRoot
			if err = rows.Scan(&r.ID, &r.Path, &r.Ready); err != nil {
				rows.Close()
				return nil, err
			}
			roots = append(roots, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out := make([]AccessBookSnapshot, len(ids))
		index := map[int64]int{}
		for i, id := range ids {
			out[i].ID = id
			index[id] = i
		}
		owners := map[int64]string{}
		private := map[int64]bool{}
		knownOwner := map[int64]bool{}
		rows, err = tx.QueryContext(ctx, `SELECT b.id,b.title,CAST(b.timestamp AS TEXT),coalesce(o.principal,''),coalesce(o.private,0),o.book_id IS NOT NULL FROM books b LEFT JOIN acl_book_owner o ON o.book_id=b.id WHERE b.id IN (SELECT value FROM json_each(?))`, param)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var title, timestamp, owner string
			var p, known bool
			if err = rows.Scan(&id, &title, &timestamp, &owner, &p, &known); err != nil {
				rows.Close()
				return nil, err
			}
			i := index[id]
			out[i].Title = title
			out[i].Timestamp = timestamp
			owners[id] = owner
			private[id] = p
			knownOwner[id] = known
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		query := `SELECT 'f:'||f.id,f.book_id,upper(f.format),f.path,f.size,coalesce(s.missing,0) FROM book_formats f LEFT JOIN source_sync_formats s ON s.format_id=f.id WHERE f.book_id IN (SELECT value FROM json_each(?)) UNION ALL SELECT 'd:'||d.rowid,d.book,upper(d.format),b.path||'/'||d.name||'.'||lower(d.format),coalesce(d.uncompressed_size,0),0 FROM data d JOIN books b ON b.id=d.book WHERE d.book IN (SELECT value FROM json_each(?)) AND NOT EXISTS(SELECT 1 FROM book_formats f WHERE f.book_id=d.book AND upper(f.format)=upper(d.format))`
		rows, err = tx.QueryContext(ctx, query, param, param)
		if err != nil {
			return nil, err
		}
		type source struct {
			key, path, format string
			book, size        int64
			missing           bool
		}
		sources := []source{}
		for rows.Next() {
			var f source
			if err = rows.Scan(&f.key, &f.book, &f.format, &f.path, &f.size, &f.missing); err != nil {
				rows.Close()
				return nil, err
			}
			sources = append(sources, f)
			if len(sources) > 512 {
				rows.Close()
				return nil, fmt.Errorf("source file budget exceeded")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, f := range sources {
			physical := f.path
			if sourcefile.IsReference(physical) {
				ref, e := sourcefile.Decode(physical)
				if e != nil {
					return nil, e
				}
				physical = ref.Archive
			}
			if !filepath.IsAbs(physical) {
				physical = filepath.Join(libraryRoot, physical)
			}
			physical = filepath.Clean(physical)
			var root AccessRoot
			dir := ""
			for _, r := range roots {
				rel, e := filepath.Rel(r.Path, physical)
				if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
					root = r
					dir = "/"
					if parent := filepath.Dir(rel); parent != "." {
						dir += filepath.ToSlash(parent)
					}
					break
				}
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO acl_source_identity(source_key,id) SELECT ?,lower(hex(randomblob(16))) WHERE NOT EXISTS(SELECT 1 FROM acl_source_identity WHERE source_key=?)`, f.key, f.key); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE acl_source_identity SET root=?,directory=? WHERE source_key=? AND (root<>? OR directory<>?)`, root.ID, dir, f.key, root.ID, dir); err != nil {
				return nil, err
			}
			var file AccessFileSnapshot
			if err = tx.QueryRowContext(ctx, `SELECT id,version FROM acl_source_identity WHERE source_key=?`, f.key).Scan(&file.ID, &file.Version); err != nil {
				return nil, err
			}
			file.Format = f.format
			file.Size = f.size
			file.Facts = access.FileFacts{Root: root.ID, Directory: dir, Ready: root.Ready && !f.missing && knownOwner[f.book], Private: private[f.book], PrivateOwner: owners[f.book]}
			var lineage string
			if e := tx.QueryRowContext(ctx, `SELECT facts FROM acl_format_lineage WHERE book_id=? AND format=?`, f.book, f.format).Scan(&lineage); e == nil {
				if len(lineage) > 1<<20 {
					return nil, fmt.Errorf("source lineage exceeds budget")
				}
				if e = json.Unmarshal([]byte(lineage), &file.Facts.Parents); e != nil {
					return nil, e
				}
			} else if e != sql.ErrNoRows {
				return nil, e
			}
			for _, set := range []struct {
				query string
				dst   *[]string
			}{{`SELECT rule_id FROM acl_source_origin WHERE file_id=?`, &file.Facts.Inherited}, {`SELECT class_id FROM acl_source_class WHERE file_id=?`, &file.Facts.Classes}} {
				r, e := tx.QueryContext(ctx, set.query, file.ID)
				if e != nil {
					return nil, e
				}
				for r.Next() {
					var value string
					if e = r.Scan(&value); e != nil {
						r.Close()
						return nil, e
					}
					*set.dst = append(*set.dst, value)
				}
				e = r.Err()
				r.Close()
				if e != nil {
					return nil, e
				}
			}
			i := index[f.book]
			out[i].Files = append(out[i].Files, file)
		}
		return out, nil
	}
}

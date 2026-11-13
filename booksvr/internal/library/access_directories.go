package library

import (
	"context"
	"database/sql"
	"path"
)

func migrateAccessDirectories(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS acl_directory_member(revision INTEGER NOT NULL,profile TEXT NOT NULL,path TEXT NOT NULL,book_id INTEGER NOT NULL,owner TEXT NOT NULL,direct INTEGER NOT NULL,nested INTEGER NOT NULL,PRIMARY KEY(revision,profile,path,book_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_directory_member_book ON acl_directory_member(revision,profile,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_directory_count(revision INTEGER NOT NULL,profile TEXT NOT NULL,parent TEXT NOT NULL,path TEXT NOT NULL,owner TEXT NOT NULL,total INTEGER NOT NULL,direct INTEGER NOT NULL,nested INTEGER NOT NULL,PRIMARY KEY(revision,profile,path,owner)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_directory_count_parent ON acl_directory_count(revision,profile,parent,path,owner)`,
		`CREATE TRIGGER IF NOT EXISTS acl_directory_delete AFTER DELETE ON acl_directory_member BEGIN UPDATE acl_directory_count SET total=total-1,direct=direct-OLD.direct,nested=nested-OLD.nested WHERE revision=OLD.revision AND profile=OLD.profile AND path=OLD.path AND owner=OLD.owner; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_directory_book_delete AFTER DELETE ON acl_profile_book BEGIN DELETE FROM acl_directory_member WHERE revision=OLD.revision AND profile=OLD.profile AND book_id=OLD.book_id; END`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func addAccessDirectories(ctx context.Context, tx *sql.Tx, revision int64, profile string, book int64, owner string, locations map[string]bool) error {
	flags := map[string]int{}
	for location := range locations {
		dir := location
		for depth := 0; dir != "" && dir != "/" && dir != "."; depth++ {
			if depth > 128 {
				return ErrAccessNotReady
			}
			flag := 2
			if dir == location {
				flag = 1
			}
			flags[dir] |= flag
			dir = path.Dir(dir)
		}
	}
	for dir, flag := range flags {
		parent := path.Dir(dir)
		if parent == "/" {
			parent = ""
		}
		direct, nested := flag&1, (flag>>1)&1
		if _, err := tx.ExecContext(ctx, `INSERT INTO acl_directory_member VALUES(?,?,?,?,?,?,?)`, revision, profile, dir, book, owner, direct, nested); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO acl_directory_count VALUES(?,?,?,?,?,1,?,?) ON CONFLICT(revision,profile,path,owner) DO UPDATE SET total=total+1,direct=direct+excluded.direct,nested=nested+excluded.nested`, revision, profile, parent, dir, owner, direct, nested); err != nil {
			return err
		}
	}
	return nil
}

func (s AccessStore) AccessDirectoryChildrenPage(ctx context.Context, scope AccessScope, parent string) ([]BookDirectory, error) {
	if !scope.valid || scope.profile == "" {
		return nil, ErrAccessNotReady
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT path,sum(total),sum(nested)>0 FROM acl_directory_count WHERE revision=? AND profile=? AND parent=? AND owner IN ('',?) GROUP BY path HAVING sum(total)>0 ORDER BY path`, scope.revision, scope.profile, parent, scope.principal)
	if err != nil {
		return nil, err
	}
	result := []BookDirectory{}
	for rows.Next() {
		var d BookDirectory
		if err = rows.Scan(&d.Path, &d.Count, &d.HasChildren); err != nil {
			rows.Close()
			return nil, err
		}
		d.Name = path.Base(d.Path)
		result = append(result, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = s.AccessCurrent(ctx, scope); err != nil {
		return nil, err
	}
	return result, nil
}

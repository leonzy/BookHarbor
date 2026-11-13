package library

import (
	"context"
	"database/sql"
)

// Group counters are maintained with the visible projection. Ordinary grouped
// browsing must not reconstruct every permitted file and work on every page.
func migrateAccessWorks(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS acl_work_member(revision INTEGER NOT NULL,profile TEXT NOT NULL,book_id INTEGER NOT NULL,work_id INTEGER NOT NULL,owner TEXT NOT NULL,PRIMARY KEY(revision,profile,book_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_work_member_group ON acl_work_member(revision,profile,work_id,owner,book_id)`,
		`CREATE INDEX IF NOT EXISTS acl_work_member_book ON acl_work_member(book_id,revision,profile)`,
		`CREATE TABLE IF NOT EXISTS acl_work_count(revision INTEGER NOT NULL,profile TEXT NOT NULL,owner TEXT NOT NULL,work_id INTEGER NOT NULL,n INTEGER NOT NULL,PRIMARY KEY(revision,profile,owner,work_id)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_work_total(revision INTEGER NOT NULL,profile TEXT NOT NULL,owner TEXT NOT NULL,duplicates INTEGER NOT NULL,PRIMARY KEY(revision,profile,owner)) WITHOUT ROWID`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_insert AFTER INSERT ON acl_work_member BEGIN INSERT INTO acl_work_count VALUES(NEW.revision,NEW.profile,NEW.owner,NEW.work_id,1) ON CONFLICT(revision,profile,owner,work_id) DO UPDATE SET n=n+1; INSERT INTO acl_work_total SELECT NEW.revision,NEW.profile,NEW.owner,CASE WHEN n>1 THEN 1 ELSE 0 END FROM acl_work_count WHERE revision=NEW.revision AND profile=NEW.profile AND owner=NEW.owner AND work_id=NEW.work_id ON CONFLICT(revision,profile,owner) DO UPDATE SET duplicates=duplicates+excluded.duplicates; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_delete AFTER DELETE ON acl_work_member BEGIN UPDATE acl_work_total SET duplicates=duplicates-CASE WHEN (SELECT n FROM acl_work_count WHERE revision=OLD.revision AND profile=OLD.profile AND owner=OLD.owner AND work_id=OLD.work_id)>1 THEN 1 ELSE 0 END WHERE revision=OLD.revision AND profile=OLD.profile AND owner=OLD.owner; UPDATE acl_work_count SET n=n-1 WHERE revision=OLD.revision AND profile=OLD.profile AND owner=OLD.owner AND work_id=OLD.work_id; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_book_delete AFTER DELETE ON acl_profile_book BEGIN DELETE FROM acl_work_member WHERE revision=OLD.revision AND profile=OLD.profile AND book_id=OLD.book_id; END`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='book_identity_keys')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	insert := func(id string) string {
		return `INSERT INTO acl_work_member SELECT p.revision,p.profile,p.book_id,k.managed_work,p.owner FROM acl_profile_book p JOIN book_identity_keys k ON k.book_id=p.book_id JOIN books b ON b.id=k.book_id AND b.work_id=k.managed_work WHERE p.book_id=` + id + ` AND k.managed_work>0;`
	}
	for _, q := range []string{
		`CREATE TRIGGER IF NOT EXISTS acl_work_book_insert AFTER INSERT ON acl_profile_book BEGIN INSERT INTO acl_work_member SELECT NEW.revision,NEW.profile,NEW.book_id,k.managed_work,NEW.owner FROM book_identity_keys k JOIN books b ON b.id=k.book_id AND b.work_id=k.managed_work WHERE k.book_id=NEW.book_id AND k.managed_work>0; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_identity_insert AFTER INSERT ON book_identity_keys BEGIN DELETE FROM acl_work_member WHERE book_id=NEW.book_id; ` + insert("NEW.book_id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_identity_delete AFTER DELETE ON book_identity_keys BEGIN DELETE FROM acl_work_member WHERE book_id=OLD.book_id; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_identity_update AFTER UPDATE OF book_id,managed_work ON book_identity_keys WHEN OLD.book_id IS NOT NEW.book_id OR OLD.managed_work IS NOT NEW.managed_work BEGIN DELETE FROM acl_work_member WHERE book_id IN (OLD.book_id,NEW.book_id); ` + insert("NEW.book_id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS acl_work_assignment AFTER UPDATE OF work_id ON books WHEN OLD.work_id IS NOT NEW.work_id BEGIN DELETE FROM acl_work_member WHERE book_id=NEW.id; ` + insert("NEW.id") + ` END`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func accessSimpleGrouped(q AccessBrowseQuery) bool {
	if !q.GroupWorks || q.Directory != nil || q.PhysicalDirectory != "" || q.FormatFilter || (q.Format != "" && q.Format != "全部") || q.Search != "" || q.Candidates != nil || q.Category != "" || q.CategoryID != 0 || len(q.Excluded) > 0 || q.SizeSort != "" || q.RatingSort {
		return false
	}
	for _, v := range q.Filters {
		if v != "" && v != "全部" {
			return false
		}
	}
	return true
}

func accessGroupedPage(ctx context.Context, db *sql.DB, scope AccessScope, q AccessBrowseQuery) ([]int64, int, error) {
	if !scope.valid || scope.profile == "" {
		return nil, 0, ErrAccessNotReady
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	args := []any{scope.revision, scope.profile, scope.principal}
	var books, duplicates, overlap int
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(books),0) FROM acl_profile_count WHERE revision=? AND profile=? AND owner IN ('',?)`, args...).Scan(&books); err != nil {
		return nil, 0, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(duplicates),0) FROM acl_work_total WHERE revision=? AND profile=? AND owner IN ('',?)`, args...).Scan(&duplicates); err != nil {
		return nil, 0, err
	}
	if scope.principal != "" {
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM acl_work_count a WHERE a.revision=? AND a.profile=? AND a.owner=? AND a.n>0 AND EXISTS(SELECT 1 FROM acl_work_count b WHERE b.revision=a.revision AND b.profile=a.profile AND b.owner='' AND b.work_id=a.work_id AND b.n>0)`, args...).Scan(&overlap); err != nil {
			return nil, 0, err
		}
	}
	order := "p.book_id DESC"
	if q.TitleSort {
		order = "p.title,p.book_id DESC"
	} else if q.TimestampSort {
		order = "p.timestamp DESC,p.book_id DESC"
	}
	if q.Start < 0 {
		q.Start = 0
	}
	if q.Size < 1 {
		q.Size = 1
	}
	if q.Size > 1000 {
		q.Size = 1000
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.book_id FROM acl_profile_book p WHERE p.revision=? AND p.profile=? AND p.owner IN ('',?) AND NOT EXISTS(SELECT 1 FROM acl_work_member m WHERE m.revision=p.revision AND m.profile=p.profile AND m.book_id=p.book_id AND EXISTS(SELECT 1 FROM acl_work_member n WHERE n.revision=m.revision AND n.profile=m.profile AND n.work_id=m.work_id AND n.owner IN ('',?) AND n.book_id<m.book_id)) ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, scope.principal, q.Size, q.Start)...)
	if err != nil {
		return nil, 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return ids, books - duplicates - overlap, (AccessStore{DB: db}).AccessCurrent(ctx, scope)
}

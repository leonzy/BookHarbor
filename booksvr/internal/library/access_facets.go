package library

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Facets are incrementally maintained from the already-authorized projection.
// Page requests read counters, never aggregate the full file/library table.
func migrateAccessFacets(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS acl_profile_facet(revision INTEGER NOT NULL,profile TEXT NOT NULL,kind TEXT NOT NULL,item TEXT NOT NULL,book_id INTEGER NOT NULL,owner TEXT NOT NULL,PRIMARY KEY(revision,profile,kind,item,book_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_profile_facet_book ON acl_profile_facet(revision,profile,book_id)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_facet_metadata ON acl_profile_facet(book_id,kind,item)`,
		`CREATE TABLE IF NOT EXISTS acl_facet_count(revision INTEGER NOT NULL,profile TEXT NOT NULL,kind TEXT NOT NULL,item TEXT NOT NULL,owner TEXT NOT NULL,n INTEGER NOT NULL,PRIMARY KEY(revision,profile,kind,item,owner)) WITHOUT ROWID`,
		`CREATE TRIGGER IF NOT EXISTS acl_facet_insert AFTER INSERT ON acl_profile_facet BEGIN UPDATE acl_facet_count SET n=n+1 WHERE revision=NEW.revision AND profile=NEW.profile AND kind=NEW.kind AND item=NEW.item AND owner=NEW.owner; INSERT INTO acl_facet_count SELECT NEW.revision,NEW.profile,NEW.kind,NEW.item,NEW.owner,1 WHERE NOT EXISTS(SELECT 1 FROM acl_facet_count WHERE revision=NEW.revision AND profile=NEW.profile AND kind=NEW.kind AND item=NEW.item AND owner=NEW.owner); END`,
		`CREATE TRIGGER IF NOT EXISTS acl_facet_delete AFTER DELETE ON acl_profile_facet BEGIN UPDATE acl_facet_count SET n=n-1 WHERE revision=OLD.revision AND profile=OLD.profile AND kind=OLD.kind AND item=OLD.item AND owner=OLD.owner; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_facet_book_delete AFTER DELETE ON acl_profile_book BEGIN DELETE FROM acl_profile_facet WHERE revision=OLD.revision AND profile=OLD.profile AND book_id=OLD.book_id; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_facet_format_insert AFTER INSERT ON acl_profile_book BEGIN INSERT INTO acl_profile_facet SELECT NEW.revision,NEW.profile,'formats',format,NEW.book_id,NEW.owner FROM acl_profile_file f WHERE revision=NEW.revision AND profile=NEW.profile AND book_id=NEW.book_id AND NOT EXISTS(SELECT 1 FROM acl_profile_facet a WHERE a.revision=NEW.revision AND a.profile=NEW.profile AND a.kind='formats' AND a.item=f.format AND a.book_id=NEW.book_id) GROUP BY format; END`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for kind, s := range categorySpecs {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, s.linkTable).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		add := func(book, item string) string {
			return fmt.Sprintf(`INSERT INTO acl_profile_facet SELECT p.revision,p.profile,'%s',CAST(%s AS TEXT),p.book_id,p.owner FROM acl_profile_book p WHERE p.book_id=%s AND NOT EXISTS(SELECT 1 FROM acl_profile_facet f WHERE f.revision=p.revision AND f.profile=p.profile AND f.kind='%s' AND f.item=CAST(%s AS TEXT) AND f.book_id=p.book_id);`, kind, item, book, kind, item)
		}
		remove := func(book, item string) string {
			return fmt.Sprintf(`DELETE FROM acl_profile_facet WHERE book_id=%s AND kind='%s' AND item=CAST(%s AS TEXT) AND NOT EXISTS(SELECT 1 FROM %s WHERE book=%s AND %s=%s);`, book, kind, item, s.linkTable, book, s.linkItemCol, item)
		}
		queries := []string{
			fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS acl_facet_book_%s AFTER INSERT ON acl_profile_book BEGIN INSERT INTO acl_profile_facet SELECT DISTINCT NEW.revision,NEW.profile,'%s',CAST(l.%s AS TEXT),NEW.book_id,NEW.owner FROM %s l WHERE l.book=NEW.book_id AND NOT EXISTS(SELECT 1 FROM acl_profile_facet f WHERE f.revision=NEW.revision AND f.profile=NEW.profile AND f.kind='%s' AND f.item=CAST(l.%s AS TEXT) AND f.book_id=NEW.book_id); END`, kind, kind, s.linkItemCol, s.linkTable, kind, s.linkItemCol),
			fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS acl_facet_%s_insert AFTER INSERT ON %s BEGIN %s END`, kind, s.linkTable, add("NEW.book", "NEW."+s.linkItemCol)),
			fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS acl_facet_%s_delete AFTER DELETE ON %s BEGIN %s END`, kind, s.linkTable, remove("OLD.book", "OLD."+s.linkItemCol)),
			fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS acl_facet_%s_update AFTER UPDATE OF book,%s ON %s WHEN OLD.book IS NOT NEW.book OR OLD.%s IS NOT NEW.%s BEGIN %s %s END`, kind, s.linkItemCol, s.linkTable, s.linkItemCol, s.linkItemCol, remove("OLD.book", "OLD."+s.linkItemCol), add("NEW.book", "NEW."+s.linkItemCol)),
		}
		for _, q := range queries {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	return nil
}

func AccessCategoryPage(ctx context.Context, db *sql.DB, scope AccessScope, category, query string, page, size int, prefixes ...string) ([]Category, int, error) {
	if !scope.valid || scope.profile == "" {
		return nil, 0, ErrAccessNotReady
	}
	if page < 1 || size < 1 || size > 1000 || page > int(^uint(0)>>1)/size {
		return nil, 0, fmt.Errorf("invalid category pagination")
	}
	base := `SELECT c.item id,c.item name,sum(c.n) cnt FROM acl_facet_count c WHERE c.revision=? AND c.profile=? AND c.kind=? AND c.owner IN ('',?) GROUP BY c.item HAVING sum(c.n)>0`
	if category != "formats" {
		s, ok := categorySpecs[category]
		if !ok {
			return nil, 0, fmt.Errorf("invalid category")
		}
		name := "i." + s.nameExpr
		if category == "ratings" {
			name = "CAST(i.rating AS TEXT)"
		}
		base = fmt.Sprintf(`SELECT i.id,%s name,sum(c.n) cnt FROM acl_facet_count c JOIN %s i ON i.id=CAST(c.item AS INTEGER) WHERE c.revision=? AND c.profile=? AND c.kind=? AND c.owner IN ('',?) GROUP BY c.item HAVING sum(c.n)>0`, name, s.itemTable)
	}
	args := []any{scope.revision, scope.profile, category, scope.principal, strings.ToLower(query)}
	filtered := `SELECT * FROM (` + base + `) WHERE instr(browse_lower(name),?)>0`
	if len(prefixes) > 0 && prefixes[0] != "" {
		filtered += ` AND browse_lower(substr(trim(name),1,length(?)))=?`
		args = append(args, prefixes[0], strings.ToLower(prefixes[0]))
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM (`+filtered+`)`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := tx.QueryContext(ctx, filtered+` ORDER BY cnt DESC,browse_lower(name),name,id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	out := []Category{}
	for rows.Next() {
		var item Category
		var id any
		if err = rows.Scan(&id, &item.Name, &item.Count); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if n, ok := id.(int64); ok {
			item.ID = n
		}
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	if err = (AccessStore{DB: db}).AccessCurrent(ctx, scope); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

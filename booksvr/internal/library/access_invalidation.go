package library

import (
	"context"
	"database/sql"
	"fmt"
)

// Invalidation is transactional with the source mutation. The unconfigured
// fast path only evaluates a small singleton/indexed-state predicate; it does
// not enqueue work or walk any book/profile tables.
func accessInvalidationSQL(id string) string {
	return `UPDATE acl_state SET source_watermark=source_watermark+1 WHERE id=1;
		UPDATE acl_dirty SET version=(SELECT source_watermark FROM acl_state WHERE id=1) WHERE book_id=` + id + `;
		INSERT INTO acl_dirty(book_id,version) SELECT ` + id + `,source_watermark FROM acl_state WHERE id=1 AND NOT EXISTS(SELECT 1 FROM acl_dirty WHERE book_id=` + id + `);
		UPDATE acl_profile SET epoch=epoch+1 WHERE EXISTS(SELECT 1 FROM acl_profile_book p WHERE p.book_id=` + id + ` AND p.revision=acl_profile.revision AND p.profile=acl_profile.profile);
		UPDATE acl_profile_count SET books=books-(SELECT count(*) FROM acl_profile_book p WHERE p.book_id=` + id + ` AND p.revision=acl_profile_count.revision AND p.profile=acl_profile_count.profile AND p.owner=acl_profile_count.owner),files=files-(SELECT count(*) FROM acl_profile_file f WHERE f.book_id=` + id + ` AND f.revision=acl_profile_count.revision AND f.profile=acl_profile_count.profile AND f.owner=acl_profile_count.owner) WHERE EXISTS(SELECT 1 FROM acl_profile_book p WHERE p.book_id=` + id + ` AND p.revision=acl_profile_count.revision AND p.profile=acl_profile_count.profile AND p.owner=acl_profile_count.owner);
		DELETE FROM acl_profile_file WHERE book_id=` + id + `;
		DELETE FROM acl_profile_book WHERE book_id=` + id + `;`
}

func migrateAccessInvalidation(tx *sql.Tx) error {
	mark := accessInvalidationSQL
	type trigger struct{ name, table, event, condition, body string }
	triggers := []trigger{
		{"owner_insert", "acl_book_owner", "INSERT", "1", mark("NEW.book_id")},
		{"owner_delete", "acl_book_owner", "DELETE", "1", mark("OLD.book_id")},
		{"owner_update", "acl_book_owner", "UPDATE OF principal,private", "OLD.principal IS NOT NEW.principal OR OLD.private IS NOT NEW.private", mark("NEW.book_id")},
		{"book_insert", "books", "INSERT", "1", mark("NEW.id")},
		{"book_delete", "books", "DELETE", "1", mark("OLD.id")},
		{"book_path", "books", "UPDATE OF path", "OLD.path IS NOT NEW.path", mark("NEW.id")},
		{"book_order", "books", "UPDATE OF title,timestamp", "OLD.title IS NOT NEW.title OR OLD.timestamp IS NOT NEW.timestamp", `UPDATE acl_profile_book SET title=NEW.title,timestamp=NEW.timestamp WHERE book_id=NEW.id;`},
		{"format_insert", "book_formats", "INSERT", "1", mark("NEW.book_id")},
		{"format_delete", "book_formats", "DELETE", "1", mark("OLD.book_id")},
		{"format_update", "book_formats", "UPDATE OF book_id,format,path,size,mtime,hash", "OLD.book_id IS NOT NEW.book_id OR OLD.format IS NOT NEW.format OR OLD.path IS NOT NEW.path OR OLD.size IS NOT NEW.size OR OLD.mtime IS NOT NEW.mtime OR OLD.hash IS NOT NEW.hash", mark("OLD.book_id") + mark("NEW.book_id")},
		{"data_insert", "data", "INSERT", "1", mark("NEW.book")},
		{"data_delete", "data", "DELETE", "1", mark("OLD.book")},
		{"data_update", "data", "UPDATE OF book,format,name,uncompressed_size", "OLD.book IS NOT NEW.book OR OLD.format IS NOT NEW.format OR OLD.name IS NOT NEW.name OR OLD.uncompressed_size IS NOT NEW.uncompressed_size", mark("OLD.book") + mark("NEW.book")},
	}
	for _, ref := range []string{"NEW", "OLD"} {
		event, name := "INSERT", "source_insert"
		if ref == "OLD" {
			event, name = "DELETE", "source_delete"
		}
		id := `(SELECT book_id FROM book_formats WHERE id=` + ref + `.format_id)`
		exists := `EXISTS(SELECT 1 FROM book_formats WHERE id=` + ref + `.format_id)`
		triggers = append(triggers, trigger{name, "source_sync_formats", event, exists, mark(id)})
		condition := `(OLD.missing IS NOT NEW.missing OR OLD.format_id IS NOT NEW.format_id) AND ` + exists
		if ref == "NEW" {
			condition = `OLD.format_id IS NOT NEW.format_id AND ` + exists
		}
		triggers = append(triggers, trigger{"source_update_" + ref, "source_sync_formats", "UPDATE OF missing,format_id", condition, mark(id)})
	}
	for _, t := range triggers {
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, t.table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		stmt := `CREATE TRIGGER IF NOT EXISTS acl_invalidate_` + t.name + ` AFTER ` + t.event + ` ON ` + t.table + ` WHEN (` + t.condition + `) AND ((SELECT active_revision FROM acl_state WHERE id=1)>0 OR EXISTS(SELECT 1 FROM acl_revision WHERE status IN ('preparing','ready'))) BEGIN ` + t.body + ` END`
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("ACL invalidation %s: %w", t.name, err)
		}
	}
	return nil
}

type AccessDirtyBook struct{ BookID, Version int64 }

func (s AccessStore) AccessDirtyBatch(ctx context.Context, limit int) ([]AccessDirtyBook, error) {
	if limit < 1 || limit > 64 {
		return nil, fmt.Errorf("invalid access batch limit")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT book_id,version FROM acl_dirty ORDER BY book_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AccessDirtyBook{}
	for rows.Next() {
		var item AccessDirtyBook
		if err = rows.Scan(&item.BookID, &item.Version); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

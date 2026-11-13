package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Visibility tracking is an outbox in the authoritative user database. Import
// writes of ordinary public books do not increment the epoch or enqueue work.
func MigrateAccessVisibility(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	mark := func(id string) string {
		return `UPDATE acl_visibility_state SET epoch=epoch+1 WHERE id=1; UPDATE acl_visibility_dirty SET version=(SELECT epoch FROM acl_visibility_state WHERE id=1) WHERE book_id=` + id + `; INSERT INTO acl_visibility_dirty SELECT ` + id + `,epoch FROM acl_visibility_state WHERE id=1 AND NOT EXISTS(SELECT 1 FROM acl_visibility_dirty WHERE book_id=` + id + `);`
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS acl_visibility_state(id INTEGER PRIMARY KEY CHECK(id=1),epoch INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO acl_visibility_state(id) VALUES(1)`,
		`CREATE TABLE IF NOT EXISTS acl_visibility_dirty(book_id INTEGER PRIMARY KEY,version INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS acl_visibility_version ON acl_visibility_dirty(version,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_account_identity(account_id INTEGER PRIMARY KEY,token TEXT NOT NULL,active INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS acl_item_owner(book_id INTEGER PRIMARY KEY,account_token TEXT NOT NULL)`,
		`INSERT INTO acl_account_identity SELECT id,lower(hex(randomblob(16))),1 FROM readers WHERE NOT EXISTS(SELECT 1 FROM acl_account_identity a WHERE a.account_id=readers.id)`,
		`CREATE TRIGGER IF NOT EXISTS acl_account_insert AFTER INSERT ON readers BEGIN UPDATE acl_account_identity SET token=lower(hex(randomblob(16))),active=1 WHERE account_id=NEW.id; INSERT INTO acl_account_identity SELECT NEW.id,lower(hex(randomblob(16))),1 WHERE NOT EXISTS(SELECT 1 FROM acl_account_identity WHERE account_id=NEW.id); UPDATE acl_visibility_state SET epoch=epoch+1 WHERE id=1; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_account_delete AFTER DELETE ON readers BEGIN UPDATE acl_account_identity SET active=0 WHERE account_id=OLD.id; UPDATE acl_visibility_state SET epoch=epoch+1 WHERE id=1; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_account_preserve_owner BEFORE DELETE ON readers BEGIN INSERT INTO acl_item_owner SELECT i.book_id,a.token FROM items i JOIN acl_account_identity a ON a.account_id=i.collector_id WHERE i.collector_id=OLD.id AND NOT EXISTS(SELECT 1 FROM acl_item_owner o WHERE o.book_id=i.book_id); END`,
		`CREATE TRIGGER IF NOT EXISTS acl_item_owner_insert AFTER INSERT ON items BEGIN UPDATE acl_item_owner SET account_token=coalesce((SELECT token FROM acl_account_identity WHERE account_id=NEW.collector_id),'orphan:'||lower(hex(randomblob(16)))) WHERE book_id=NEW.book_id; INSERT INTO acl_item_owner SELECT NEW.book_id,coalesce((SELECT token FROM acl_account_identity WHERE account_id=NEW.collector_id),'orphan:'||lower(hex(randomblob(16)))) WHERE NOT EXISTS(SELECT 1 FROM acl_item_owner WHERE book_id=NEW.book_id); END`,
		`CREATE TRIGGER IF NOT EXISTS acl_item_owner_update AFTER UPDATE OF collector_id,book_id ON items WHEN OLD.collector_id IS NOT NEW.collector_id OR OLD.book_id IS NOT NEW.book_id BEGIN DELETE FROM acl_item_owner WHERE book_id=OLD.book_id; INSERT INTO acl_item_owner SELECT NEW.book_id,coalesce((SELECT token FROM acl_account_identity WHERE account_id=NEW.collector_id),'orphan:'||lower(hex(randomblob(16)))) WHERE NOT EXISTS(SELECT 1 FROM acl_item_owner WHERE book_id=NEW.book_id); END`,
		`CREATE TRIGGER IF NOT EXISTS acl_item_owner_delete AFTER DELETE ON items BEGIN DELETE FROM acl_item_owner WHERE book_id=OLD.book_id; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_account_permission AFTER UPDATE OF admin,active,permission ON readers WHEN OLD.admin IS NOT NEW.admin OR OLD.active IS NOT NEW.active OR OLD.permission IS NOT NEW.permission BEGIN UPDATE acl_visibility_state SET epoch=epoch+1 WHERE id=1; END`,
		`CREATE TRIGGER IF NOT EXISTS acl_visibility_insert AFTER INSERT ON items WHEN NEW.scope='private' BEGIN ` + mark("NEW.book_id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS acl_visibility_delete AFTER DELETE ON items WHEN OLD.scope='private' BEGIN ` + mark("OLD.book_id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS acl_visibility_update AFTER UPDATE OF scope,collector_id,book_id ON items WHEN (OLD.scope='private' OR NEW.scope='private') AND (OLD.scope IS NOT NEW.scope OR OLD.collector_id IS NOT NEW.collector_id OR OLD.book_id IS NOT NEW.book_id) BEGIN ` + mark("OLD.book_id") + mark("NEW.book_id") + ` END`,
	} {
		if _, err = tx.Exec(q); err != nil {
			return err
		}
	}
	var podcast bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='podcast_subscriptions')`).Scan(&podcast); err != nil {
		return err
	}
	if podcast {
		if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS acl_account_revoke_podcast BEFORE DELETE ON readers BEGIN UPDATE podcast_subscriptions SET active=0,token_hash='',revoked_at=datetime('now') WHERE reader_id=OLD.id; END`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type AccessOwner struct {
	BookID, AccountID int64
	Private           bool
	AccountToken      string
}

func AccessOwners(ctx context.Context, db *sql.DB, ids []int64) ([]AccessOwner, int64, error) {
	if len(ids) > 64 {
		return nil, 0, fmt.Errorf("owner batch exceeds budget")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	owners, epoch, err := AccessOwnersTx(ctx, tx, ids)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return owners, epoch, nil
}

func AccessOwnersTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]AccessOwner, int64, error) {
	if len(ids) > 64 {
		return nil, 0, fmt.Errorf("owner batch exceeds budget")
	}
	var err error
	var epoch int64
	if err = tx.QueryRowContext(ctx, `SELECT epoch FROM acl_visibility_state WHERE id=1`).Scan(&epoch); err != nil {
		return nil, 0, err
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT requested.value,coalesce(i.collector_id,0),coalesce(i.scope='private',0),coalesce(o.account_token,a.token,'') FROM json_each(?) requested LEFT JOIN items i ON i.book_id=requested.value LEFT JOIN acl_item_owner o ON o.book_id=i.book_id LEFT JOIN acl_account_identity a ON a.account_id=i.collector_id`, string(encoded))
	if err != nil {
		return nil, 0, err
	}
	out := []AccessOwner{}
	for rows.Next() {
		var o AccessOwner
		if err = rows.Scan(&o.BookID, &o.AccountID, &o.Private, &o.AccountToken); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	return out, epoch, nil
}

func AccessVisibilityEpoch(ctx context.Context, db *sql.DB) (int64, error) {
	var epoch int64
	err := db.QueryRowContext(ctx, `SELECT epoch FROM acl_visibility_state WHERE id=1`).Scan(&epoch)
	return epoch, err
}

func AccessAccountToken(ctx context.Context, db *sql.DB, id int64) (string, error) {
	if id == 0 {
		return "guest", nil
	}
	var token string
	err := db.QueryRowContext(ctx, `SELECT token FROM acl_account_identity WHERE account_id=? AND active=1`, id).Scan(&token)
	return token, err
}

func AccessVisibilityChanges(ctx context.Context, db *sql.DB, after int64) ([]int64, int64, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, after, err
	}
	defer tx.Rollback()
	var epoch int64
	if err = tx.QueryRowContext(ctx, `SELECT epoch FROM acl_visibility_state WHERE id=1`).Scan(&epoch); err != nil {
		return nil, after, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT book_id,version FROM acl_visibility_dirty WHERE version>? ORDER BY version,book_id LIMIT 64`, after)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	ids := []int64{}
	watermark := after
	for rows.Next() {
		var id, v int64
		if err = rows.Scan(&id, &v); err != nil {
			return nil, after, err
		}
		ids = append(ids, id)
		watermark = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, after, err
	}
	if len(ids) == 0 {
		watermark = epoch
	}
	if err = tx.Commit(); err != nil {
		return nil, after, err
	}
	return ids, watermark, nil
}

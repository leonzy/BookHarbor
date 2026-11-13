package library

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"talebook/booksvr/internal/access"
)

var (
	ErrAccessConflict = errors.New("access.conflict")
	ErrAccessNotReady = errors.New("access.not_ready")
)

// AccessStore keeps policy publication and its projections in the library DB.
// Draft preparation never activates a rule. Publication requires the runtime
// capability gate and an exact, current preparation checkpoint.
type AccessStore struct{ DB *sql.DB }

func MigrateAccess(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS acl_state(id INTEGER PRIMARY KEY CHECK(id=1),active_revision INTEGER NOT NULL DEFAULT 0,policy_epoch INTEGER NOT NULL DEFAULT 0,source_watermark INTEGER NOT NULL DEFAULT 0,minimum_reader INTEGER NOT NULL DEFAULT 1)`,
		`INSERT OR IGNORE INTO acl_state(id) VALUES(1)`,
		`CREATE TABLE IF NOT EXISTS acl_principal(id TEXT PRIMARY KEY,account_id INTEGER UNIQUE,guest INTEGER NOT NULL DEFAULT 0,disabled INTEGER NOT NULL DEFAULT 0,account_token TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS acl_principal_token ON acl_principal(account_token) WHERE account_token<>''`,
		`CREATE TABLE IF NOT EXISTS acl_revision(id INTEGER PRIMARY KEY AUTOINCREMENT,version INTEGER NOT NULL DEFAULT 1,base_revision INTEGER NOT NULL,policy TEXT NOT NULL,status TEXT NOT NULL,processed INTEGER NOT NULL DEFAULT 0,total INTEGER NOT NULL DEFAULT 0,watermark INTEGER NOT NULL DEFAULT 0,message TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS acl_revision_status ON acl_revision(status,id)`,
		`CREATE TABLE IF NOT EXISTS acl_profile(revision INTEGER NOT NULL,profile TEXT NOT NULL,unrestricted INTEGER NOT NULL DEFAULT 0,epoch INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(revision,profile)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_profile_principal(revision INTEGER NOT NULL,principal TEXT NOT NULL,profile TEXT NOT NULL,PRIMARY KEY(revision,principal)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_profile_file(revision INTEGER NOT NULL,profile TEXT NOT NULL,file_id TEXT NOT NULL,book_id INTEGER NOT NULL,format TEXT NOT NULL,source_root TEXT NOT NULL,directory TEXT NOT NULL,physical_directory TEXT NOT NULL DEFAULT '',size INTEGER NOT NULL,source_version INTEGER NOT NULL,owner TEXT NOT NULL DEFAULT '',PRIMARY KEY(revision,profile,file_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_profile_file_physical_directory ON acl_profile_file(revision,profile,physical_directory,book_id)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_file_book ON acl_profile_file(revision,profile,book_id,format)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_file_invalidate ON acl_profile_file(book_id,revision,profile)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_file_directory ON acl_profile_file(revision,profile,source_root,directory,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_profile_book(revision INTEGER NOT NULL,profile TEXT NOT NULL,book_id INTEGER NOT NULL,title TEXT NOT NULL,timestamp TEXT NOT NULL,size INTEGER NOT NULL,owner TEXT NOT NULL DEFAULT '',PRIMARY KEY(revision,profile,book_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_title ON acl_profile_book(revision,profile,title,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_newest ON acl_profile_book(revision,profile,timestamp DESC,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_size ON acl_profile_book(revision,profile,size DESC,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_size_desc ON acl_profile_book(revision,profile,(size>0) DESC,size DESC,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_size_asc ON acl_profile_book(revision,profile,(size>0) DESC,size,book_id DESC)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_owner ON acl_profile_book(revision,profile,owner,book_id)`,
		`CREATE INDEX IF NOT EXISTS acl_profile_book_invalidate ON acl_profile_book(book_id,revision,profile)`,
		`CREATE TABLE IF NOT EXISTS acl_profile_count(revision INTEGER NOT NULL,profile TEXT NOT NULL,owner TEXT NOT NULL,books INTEGER NOT NULL DEFAULT 0,files INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(revision,profile,owner)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS acl_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,revision INTEGER NOT NULL,actor TEXT NOT NULL,action TEXT NOT NULL,created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS acl_class_membership(revision INTEGER NOT NULL,book_id INTEGER NOT NULL,class_id TEXT NOT NULL,PRIMARY KEY(revision,book_id,class_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_class_membership_class ON acl_class_membership(revision,class_id,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_class_edit(revision INTEGER NOT NULL,book_id INTEGER NOT NULL,class_id TEXT NOT NULL,assigned INTEGER NOT NULL,PRIMARY KEY(revision,book_id,class_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS acl_class_edit_class ON acl_class_edit(revision,class_id,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_dirty(book_id INTEGER PRIMARY KEY,version INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS acl_dirty_version ON acl_dirty(version,book_id)`,
		`CREATE TABLE IF NOT EXISTS acl_build(revision INTEGER PRIMARY KEY,version INTEGER NOT NULL,cursor INTEGER NOT NULL DEFAULT 0,ceiling INTEGER NOT NULL DEFAULT 0,watermark INTEGER NOT NULL DEFAULT 0,phase TEXT NOT NULL DEFAULT 'initial')`,
		`CREATE TABLE IF NOT EXISTS acl_worker_lease(id INTEGER PRIMARY KEY CHECK(id=1),owner TEXT NOT NULL,expires INTEGER NOT NULL)`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return fmt.Errorf("access schema: %w", err)
		}
	}
	if err = migrateAccessSources(tx); err != nil {
		return err
	}
	if err = migrateAccessInvalidation(tx); err != nil {
		return err
	}
	if err = migrateAccessFacets(tx); err != nil {
		return err
	}
	if err = migrateAccessDirectories(tx); err != nil {
		return err
	}
	if err = migrateAccessWorks(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type AccessRevision struct {
	ID           int64         `json:"id"`
	Version      int64         `json:"version"`
	BaseRevision int64         `json:"base_revision"`
	Policy       access.Policy `json:"policy"`
	Status       string        `json:"status"`
	Processed    int64         `json:"processed"`
	Total        int64         `json:"total"`
	Watermark    int64         `json:"watermark"`
	Message      string        `json:"message"`
	UpdatedAt    int64         `json:"updated_at"`
}

func accessID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// RegisterPrincipal does not derive an ACL identity from a reusable account ID.
// Call ForgetPrincipal in the same account-deletion coordination before any
// account ID can be reused. Existing IDs survive normal service restarts.
func (s AccessStore) RegisterPrincipal(ctx context.Context, accountID int64, accountToken ...string) (string, error) {
	if accountID < 0 {
		return "", fmt.Errorf("invalid account")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Serialize with account mapping updates; a plain read-then-write snapshot
	// can otherwise fail on SQLite's deferred transaction upgrade.
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return "", err
	}
	var id, existingToken string
	token := ""
	if len(accountToken) > 0 {
		token = accountToken[0]
	}
	err = tx.QueryRowContext(ctx, `SELECT id,account_token FROM acl_principal WHERE account_id=?`, accountID).Scan(&id, &existingToken)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if err == nil && token != "" && existingToken != "" && token != existingToken {
		if _, err = tx.ExecContext(ctx, `UPDATE acl_principal SET disabled=1,account_id=NULL WHERE id=?`, id); err != nil {
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch+1 WHERE id=1`); err != nil {
			return "", err
		}
		err = sql.ErrNoRows
	} else if err == nil && token != "" && existingToken == "" {
		if _, err = tx.ExecContext(ctx, `UPDATE acl_principal SET account_token=? WHERE id=?`, token, id); err != nil {
			return "", err
		}
	}
	if err == sql.ErrNoRows {
		id, err = accessID()
		if err != nil {
			return "", err
		}
		guest := 0
		if accountID == 0 {
			guest = 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO acl_principal(id,account_id,guest,account_token) VALUES(?,?,?,?)`, id, accountID, guest, token); err != nil {
			return "", err
		}
		var active, version int64
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT r.id,r.version,r.policy FROM acl_state s JOIN acl_revision r ON r.id=s.active_revision WHERE s.id=1`).Scan(&active, &version, &raw)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if err == nil {
			var policy access.Policy
			if err = json.Unmarshal([]byte(raw), &policy); err != nil {
				return "", err
			}
			key := policy.WithRevisionOrigins(active, version).Compile(id, false).Key()
			if _, err = tx.ExecContext(ctx, `INSERT INTO acl_profile_principal(revision,principal,profile) SELECT revision,?,profile FROM acl_profile WHERE revision=? AND profile=?`, id, active, key); err != nil {
				return "", err
			}
		}
	}
	return id, tx.Commit()
}

func (s AccessStore) ForgetPrincipal(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return fmt.Errorf("cannot delete guest mapping")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch+1 WHERE id=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE acl_principal SET disabled=1,account_id=NULL WHERE account_id=?`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func accessPrincipals(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM acl_principal WHERE disabled=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// SaveDraft uses optimistic concurrency and never mutates the active policy.
func (s AccessStore) SaveDraft(ctx context.Context, id, version, base int64, actor string, p access.Policy) (AccessRevision, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AccessRevision{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return AccessRevision{}, err
	}
	var active int64
	if err = tx.QueryRowContext(ctx, `SELECT active_revision FROM acl_state WHERE id=1`).Scan(&active); err != nil {
		return AccessRevision{}, err
	}
	if active != base {
		return AccessRevision{}, ErrAccessConflict
	}
	if active > 0 {
		var prior access.Policy
		var raw string
		var priorVersion int64
		if err = tx.QueryRowContext(ctx, `SELECT policy,version FROM acl_revision WHERE id=?`, active).Scan(&raw, &priorVersion); err != nil {
			return AccessRevision{}, err
		}
		if err = json.Unmarshal([]byte(raw), &prior); err != nil {
			return AccessRevision{}, err
		}
		prior = prior.WithRevisionOrigins(active, priorVersion)
		// Historical restrictions are server-owned, not removable via a draft.
		p.OriginRules = prior.OriginRules
	}
	ids, err := accessPrincipals(ctx, tx)
	if err != nil {
		return AccessRevision{}, err
	}
	if !ids[actor] {
		return AccessRevision{}, fmt.Errorf("unknown actor")
	}
	if err = p.Validate(ids); err != nil {
		return AccessRevision{}, err
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return AccessRevision{}, err
	}
	now := time.Now().Unix()
	if id == 0 {
		if version != 0 {
			return AccessRevision{}, ErrAccessConflict
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO acl_revision(base_revision,policy,status,created_by,created_at,updated_at) VALUES(?,?,'draft',?,?,?)`, base, string(blob), actor, now, now)
		if e != nil {
			return AccessRevision{}, e
		}
		id, err = res.LastInsertId()
		if err != nil {
			return AccessRevision{}, err
		}
	} else {
		res, e := tx.ExecContext(ctx, `UPDATE acl_revision SET policy=?,version=version+1,status='draft',processed=0,total=0,message='',updated_at=? WHERE id=? AND version=? AND base_revision=? AND status IN ('draft','failed','cancelled','ready')`, string(blob), now, id, version, base)
		if e != nil {
			return AccessRevision{}, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return AccessRevision{}, e
		}
		if n != 1 {
			return AccessRevision{}, ErrAccessConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO acl_audit(revision,actor,action,created_at) VALUES(?,?,'save_draft',?)`, id, actor, now); err != nil {
		return AccessRevision{}, err
	}
	if err = tx.Commit(); err != nil {
		return AccessRevision{}, err
	}
	return s.Revision(ctx, id)
}

func (s AccessStore) Revision(ctx context.Context, id int64) (AccessRevision, error) {
	var r AccessRevision
	var blob string
	err := s.DB.QueryRowContext(ctx, `SELECT id,version,base_revision,policy,status,processed,total,watermark,message,updated_at FROM acl_revision WHERE id=?`, id).Scan(&r.ID, &r.Version, &r.BaseRevision, &blob, &r.Status, &r.Processed, &r.Total, &r.Watermark, &r.Message, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(blob), &r.Policy)
	if err == nil {
		r.Policy = r.Policy.WithRevisionOrigins(r.ID, r.Version)
	}
	return r, err
}

func (s AccessStore) BeginPreparation(ctx context.Context, id, version int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE acl_revision SET status='preparing',message='',updated_at=? WHERE id=? AND version=? AND status IN ('draft','failed','cancelled') AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1) AND NOT EXISTS(SELECT 1 FROM acl_revision other WHERE other.id<>acl_revision.id AND other.status IN ('preparing','ready'))`, time.Now().Unix(), id, version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccessConflict
	}
	return nil
}

func (s AccessStore) CancelPreparation(ctx context.Context, id, version int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE acl_revision SET status='cancelled',version=version+1,updated_at=? WHERE id=? AND version=? AND status IN ('preparing','ready','failed')`, time.Now().Unix(), id, version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccessConflict
	}
	return nil
}

// AccessBuildToken fences worker writes after cancellation/edit/restart. It is
// persisted, not an in-memory goroutine flag.
type AccessBuildToken struct {
	Revision, Version, Watermark int64
	Lease                        string
}

func accessLeaseCurrent(ctx context.Context, tx *sql.Tx, t AccessBuildToken) error {
	if t.Lease == "" {
		return nil
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_worker_lease WHERE id=1 AND owner=? AND expires>?)`, t.Lease, time.Now().UnixMilli()).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrAccessConflict
	}
	return nil
}

func accessBuildCurrent(ctx context.Context, tx *sql.Tx, t AccessBuildToken) error {
	if err := accessLeaseCurrent(ctx, tx, t); err != nil {
		return err
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acl_revision WHERE id=? AND version=? AND status='preparing' AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1)`, t.Revision, t.Version).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccessConflict
	}
	return nil
}

func accessProjectionCurrent(ctx context.Context, tx *sql.Tx, t AccessBuildToken) error {
	if err := accessLeaseCurrent(ctx, tx, t); err != nil {
		return err
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acl_revision WHERE id=? AND version=? AND ((status IN ('preparing','ready') AND base_revision=(SELECT active_revision FROM acl_state WHERE id=1)) OR (status='active' AND id=(SELECT active_revision FROM acl_state WHERE id=1)))`, t.Revision, t.Version).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccessConflict
	}
	return nil
}

// FinishPreparation requires the observed source watermark to remain current.
// The builder must also verify all projection invariants before calling it.
func (s AccessStore) FinishPreparation(ctx context.Context, t AccessBuildToken) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acl_state SET policy_epoch=policy_epoch WHERE id=1`); err != nil {
		return err
	}
	if err = accessBuildCurrent(ctx, tx, t); err != nil {
		return err
	}
	var watermark int64
	if err = tx.QueryRowContext(ctx, `SELECT source_watermark FROM acl_state WHERE id=1`).Scan(&watermark); err != nil {
		return err
	}
	if watermark != t.Watermark {
		return ErrAccessNotReady
	}
	var complete bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_build WHERE revision=? AND version=? AND phase='delta' AND NOT EXISTS(SELECT 1 FROM acl_dirty WHERE version>acl_build.watermark))`, t.Revision, t.Version).Scan(&complete); err != nil {
		return err
	}
	if !complete {
		return ErrAccessNotReady
	}
	var unprepared int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM acl_principal u WHERE u.disabled=0 AND NOT EXISTS(SELECT 1 FROM acl_profile_principal m JOIN acl_profile p ON p.revision=m.revision AND p.profile=m.profile WHERE m.revision=? AND m.principal=u.id)`, t.Revision).Scan(&unprepared); err != nil {
		return err
	}
	if unprepared != 0 {
		return ErrAccessNotReady
	}
	if _, err = tx.ExecContext(ctx, `UPDATE acl_revision SET status='ready',watermark=?,updated_at=? WHERE id=?`, watermark, time.Now().Unix(), t.Revision); err != nil {
		return err
	}
	return tx.Commit()
}

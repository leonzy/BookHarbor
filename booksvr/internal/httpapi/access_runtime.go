package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

// accessRuntime coordinates the authoritative user DB visibility epoch with
// library projections. An out-of-date mirror fails closed before serving data.
type accessRuntime struct {
	store       library.AccessStore
	users       *sql.DB
	libraryRoot string
}

func (r *accessRuntime) snapshot(ctx context.Context, tx *sql.Tx, ids []int64) ([]library.AccessBookSnapshot, error) {
	var owners []userdb.AccessOwner
	var err error
	if r.users == r.store.DB {
		owners, _, err = userdb.AccessOwnersTx(ctx, tx, ids)
	} else {
		owners, _, err = userdb.AccessOwners(ctx, r.users, ids)
	}
	if err != nil {
		return nil, err
	}
	if err = r.writeOwners(ctx, tx, owners); err != nil {
		return nil, err
	}
	return library.AccessSourceReader(r.libraryRoot)(ctx, tx, ids)
}

func (r *accessRuntime) syncAccounts(ctx context.Context) error {
	readers, err := userdb.ListReaders(r.users)
	if err != nil {
		return err
	}
	wanted := map[int64]string{0: "guest"}
	for _, reader := range readers {
		token, e := userdb.AccessAccountToken(ctx, r.users, reader.ID)
		if e != nil {
			return e
		}
		wanted[reader.ID] = token
	}
	for accountID, token := range wanted {
		var exists bool
		if e := r.store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acl_principal WHERE account_id=? AND account_token=? AND disabled=0)`, accountID, token).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			if _, e := r.store.RegisterPrincipal(ctx, accountID, token); e != nil {
				return e
			}
		}
	}
	rows, err := r.store.DB.QueryContext(ctx, `SELECT account_id FROM acl_principal WHERE disabled=0 AND account_id IS NOT NULL`)
	if err != nil {
		return err
	}
	var removed []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if _, ok := wanted[id]; !ok {
			removed = append(removed, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range removed {
		if err = r.store.ForgetPrincipal(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func startAccessRuntime(ctx context.Context, opts ServeOptions, db, users *sql.DB) (*accessRuntime, func(), error) {
	r := &accessRuntime{store: library.AccessStore{DB: db}, users: users, libraryRoot: opts.LibraryRoot}
	primary := !opts.ReaderOnly && !opts.PluginFeaturesOnly && !opts.MetadataOnly
	if !primary {
		// Sidecars may start before the primary creates the schema. Keep the
		// resolver attached: each request checks readiness instead of retaining
		// a permanent legacy bypass for the lifetime of that process.
		return r, func() {}, nil
	}
	if err := library.MigrateAccess(db); err != nil {
		return nil, nil, err
	}
	if err := createUserTables(users, "sqlite"); err != nil {
		return nil, nil, err
	}
	if err := userdb.MigrateAccessVisibility(users); err != nil {
		return nil, nil, err
	}
	if err := r.syncAccounts(ctx); err != nil {
		return nil, nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker := library.AccessWorker{Store: r.store, Read: r.snapshot, ReadWrites: true, BatchSize: 32}
		delay := time.Duration(0)
		var refreshed time.Time
		for {
			timer := time.NewTimer(delay)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			var err error
			if time.Since(refreshed) > 30*time.Second {
				err = r.syncAccounts(workerCtx)
				if err == nil {
					err = r.store.SyncAccessRoots(workerCtx, opts.LibraryRoot)
				}
				if err == nil {
					refreshed = time.Now()
				}
			}
			worked := false
			if err == nil {
				worked, err = r.syncVisibility(workerCtx)
			}
			if err == nil {
				var advanced bool
				advanced, err = r.store.RefreshAccessRootBatch(workerCtx)
				worked = worked || advanced
			}
			if err == nil {
				var advanced bool
				advanced, err = worker.Tick(workerCtx)
				worked = worked || advanced
			}
			if workerCtx.Err() != nil {
				return
			}
			delay = 5 * time.Second
			if err != nil {
				if opts.Out != nil {
					fmt.Fprintf(opts.Out, "access maintenance: %v\n", err)
				}
			} else if worked {
				delay = 50 * time.Millisecond
			}
		}
	}()
	return r, func() { cancel(); <-done }, nil
}

func (r *accessRuntime) writeOwners(ctx context.Context, tx *sql.Tx, owners []userdb.AccessOwner) error {
	for _, o := range owners {
		principal := ""
		if o.Private {
			err := tx.QueryRowContext(ctx, `SELECT id FROM acl_principal WHERE account_token=? AND account_token<>''`, o.AccountToken).Scan(&principal)
			if err == sql.ErrNoRows {
				principal = fmt.Sprintf("orphan:%s:%d", o.AccountToken, o.AccountID)
			} else if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO acl_book_owner(book_id,principal,private) VALUES(?,?,?) ON CONFLICT(book_id) DO UPDATE SET principal=excluded.principal,private=excluded.private WHERE acl_book_owner.principal IS NOT excluded.principal OR acl_book_owner.private IS NOT excluded.private`, o.BookID, principal, o.Private); err != nil {
			return err
		}
	}
	return nil
}

func (r *accessRuntime) syncVisibility(ctx context.Context) (bool, error) {
	var watermark int64
	if err := r.store.DB.QueryRowContext(ctx, `SELECT watermark FROM acl_user_sync WHERE id=1`).Scan(&watermark); err != nil {
		return false, err
	}
	ids, next, err := userdb.AccessVisibilityChanges(ctx, r.users, watermark)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 && next == watermark {
		return false, nil
	}
	owners, _, err := userdb.AccessOwners(ctx, r.users, ids)
	if err != nil {
		return false, err
	}
	tx, err := r.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Fence concurrent mirror workers before touching owner records.
	res, err := tx.ExecContext(ctx, `UPDATE acl_user_sync SET watermark=? WHERE id=1 AND watermark=?`, next, watermark)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, library.ErrAccessConflict
	}
	if err = r.writeOwners(ctx, tx, owners); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (r *accessRuntime) visibilityCurrent(ctx context.Context) (int64, error) {
	epoch, err := userdb.AccessVisibilityEpoch(ctx, r.users)
	if err != nil {
		return 0, err
	}
	var mirrored int64
	if err = r.store.DB.QueryRowContext(ctx, `SELECT watermark FROM acl_user_sync WHERE id=1`).Scan(&mirrored); err != nil {
		return 0, err
	}
	if mirrored != epoch {
		return 0, library.ErrAccessNotReady
	}
	return epoch, nil
}

func (r *accessRuntime) prepare(ctx *Context) (func() error, error) {
	c := ctx.Request.Context()
	var available bool
	if err := r.store.DB.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='acl_state')`).Scan(&available); err != nil {
		return nil, err
	}
	if !available {
		return func() error {
			var appeared bool
			if err := r.store.DB.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='acl_state')`).Scan(&appeared); err != nil {
				return err
			}
			if appeared {
				return library.ErrAccessConflict
			}
			return nil
		}, nil
	}
	token, err := userdb.AccessAccountToken(c, r.users, ctx.Identity.UserID)
	if err != nil {
		return nil, err
	}
	var bound bool
	if err = r.store.DB.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM acl_principal WHERE account_id=? AND account_token=? AND disabled=0)`, ctx.Identity.UserID, token).Scan(&bound); err != nil {
		return nil, err
	}
	if !bound {
		if _, err = r.store.RegisterPrincipal(c, ctx.Identity.UserID, token); err != nil {
			return nil, err
		}
	}
	scope, err := r.store.ResolveAccess(c, ctx.Identity.UserID, ctx.Identity.IsAdmin)
	if err != nil {
		return nil, err
	}
	ctx.Access = &scope
	if scope.Revision() == 0 {
		return func() error { return r.store.AccessCurrent(c, scope) }, nil
	}
	epoch, err := r.visibilityCurrent(c)
	if err != nil {
		return nil, err
	}
	return func() error {
		if err := r.store.AccessCurrent(c, scope); err != nil {
			return err
		}
		current, err := userdb.AccessVisibilityEpoch(c, r.users)
		if err != nil {
			return err
		}
		if current != epoch {
			return library.ErrAccessConflict
		}
		return nil
	}, nil
}

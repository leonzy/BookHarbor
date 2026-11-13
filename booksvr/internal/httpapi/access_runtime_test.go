package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

func TestAccessSidecarAttachesBeforePrimaryMigration(t *testing.T) {
	db := netSchemaDB(t)
	r, stop, err := startAccessRuntime(context.Background(), ServeOptions{ReaderOnly: true}, db, db)
	if err != nil || r == nil {
		t.Fatal("sidecar lost resolver", err)
	}
	defer stop()
	ctx := &Context{Request: httptest.NewRequest("GET", "/", nil), DB: db}
	check, err := r.prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = check(); err != nil {
		t.Fatal(err)
	}
	if err = library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if err = check(); !errors.Is(err, library.ErrAccessConflict) {
		t.Fatal("legacy response survived schema activation", err)
	}
	if err = userdb.MigrateAccessVisibility(db); err != nil {
		t.Fatal(err)
	}
	check, err = r.prepare(ctx)
	if err != nil || ctx.Access == nil {
		t.Fatal("new request not protected", err)
	}
	if err = check(); err != nil {
		t.Fatal(err)
	}
}

func TestAccessVisibilityMirrorFencesPrivateChange(t *testing.T) {
	users := netSchemaDB(t)
	bookSeedReader(t, users, 1, "original")
	if err := userdb.MigrateAccessVisibility(users); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := &accessRuntime{store: library.AccessStore{DB: db}, users: users}
	token, err := userdb.AccessAccountToken(ctx, users, 1)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := r.store.RegisterPrincipal(ctx, 1, token)
	if err != nil {
		t.Fatal(err)
	}
	bookSeedItem(t, users, 10, "public", 1)
	if _, err = r.visibilityCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if err = userdb.SetItemScope(users, 10, "private"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.visibilityCurrent(ctx); !errors.Is(err, library.ErrAccessNotReady) {
		t.Fatal("stale public mirror accepted", err)
	}
	worked, err := r.syncVisibility(ctx)
	if err != nil || !worked {
		t.Fatal(worked, err)
	}
	if _, err = r.visibilityCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	var private bool
	if err = db.QueryRow(`SELECT principal,private FROM acl_book_owner WHERE book_id=10`).Scan(&owner, &private); err != nil || owner != actor || !private {
		t.Fatal(owner, private, err)
	}
	if err = userdb.DeleteReader(users, 1); err != nil {
		t.Fatal(err)
	}
	bookSeedReader(t, users, 1, "replacement")
	newToken, err := userdb.AccessAccountToken(ctx, users, 1)
	if err != nil || newToken == token {
		t.Fatal("account token reused", newToken, err)
	}
	newActor, err := r.store.RegisterPrincipal(ctx, 1, newToken)
	if err != nil || newActor == actor {
		t.Fatal("principal authority reused", newActor, err)
	}
	owners, _, err := userdb.AccessOwners(ctx, users, []int64{10})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.writeOwners(ctx, tx, owners); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT principal FROM acl_book_owner WHERE book_id=10`).Scan(&owner); err != nil || owner != actor {
		t.Fatal("replacement account inherited private book", owner, err)
	}
	if err = userdb.SetItemScope(users, 10, "public"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.visibilityCurrent(ctx); !errors.Is(err, library.ErrAccessNotReady) {
		t.Fatal("unmirrored visibility change accepted", err)
	}
	if _, err = r.syncVisibility(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT principal,private FROM acl_book_owner WHERE book_id=10`).Scan(&owner, &private); err != nil || owner != "" || private {
		t.Fatal(owner, private, err)
	}
	worked, err = r.syncVisibility(ctx)
	if err != nil || worked {
		t.Fatal("idle mirror performed work", worked, err)
	}
}

func TestAccessAccountSynchronizationIsIdleAndTombstonesDeletedUsers(t *testing.T) {
	db := netSchemaDB(t)
	bookSeedReader(t, db, 1, "one")
	if err := library.MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if err := userdb.MigrateAccessVisibility(db); err != nil {
		t.Fatal(err)
	}
	r := &accessRuntime{store: library.AccessStore{DB: db}, users: db}
	ctx := context.Background()
	if err := r.syncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := r.syncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatal("idle account sync wrote", before, after, err)
	}
	if err := userdb.DeleteReader(db, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.syncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM acl_principal WHERE disabled=1 AND account_id IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

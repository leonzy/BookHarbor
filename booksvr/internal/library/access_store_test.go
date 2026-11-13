package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"talebook/booksvr/internal/access"
)

func accessTestStore(t *testing.T) (AccessStore, string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err = MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if err = MigrateAccess(db); err != nil {
		t.Fatal("migration not idempotent:", err)
	}
	s := AccessStore{DB: db}
	id, err := s.RegisterPrincipal(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

// Projection unit tests supply snapshots directly. Only worker tests exercise
// the real scan; this fixture records completion of that explicit snapshot.
func accessTestCheckpoint(t *testing.T, s AccessStore, token AccessBuildToken) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT OR REPLACE INTO acl_build(revision,version,watermark,phase) VALUES(?,?,?,'delta')`, token.Revision, token.Version, token.Watermark); err != nil {
		t.Fatal(err)
	}
}

func TestAccessDraftOptimisticConcurrencyAndNoActivation(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	p := access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.DenyAll}}}}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "draft" || r.Version != 1 {
		t.Fatalf("unexpected draft: %+v", r)
	}
	changed, err := s.SaveDraft(ctx, r.ID, r.Version, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != 2 {
		t.Fatal("version did not advance")
	}
	if _, err = s.SaveDraft(ctx, r.ID, r.Version, 0, actor, p); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("lost update accepted", err)
	}
	var active int
	if err = s.DB.QueryRow(`SELECT active_revision FROM acl_state`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("saving draft activated policy")
	}
}

func TestAccessCancellationFencesOldWorker(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version}); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("cancelled worker became ready", err)
	}
	r, err = s.Revision(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "cancelled" || r.Version != 2 {
		t.Fatalf("unexpected state %+v", r)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version}); err != nil {
		if !errors.Is(err, ErrAccessNotReady) {
			t.Fatal(err)
		}
	} else {
		t.Fatal("unbuilt profiles marked ready")
	}
	if _, err = s.BuildAccessProfiles(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version}, []string{actor}); err != nil {
		t.Fatal(err)
	}
	accessTestCheckpoint(t, s, AccessBuildToken{Revision: r.ID, Version: r.Version})
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version}); err != nil {
		t.Fatal(err)
	}
}

func TestAccessSourceChangesPreventStaleReady(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE acl_state SET source_watermark=7`); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version, Watermark: 6}); !errors.Is(err, ErrAccessNotReady) {
		t.Fatal("stale ready allowed", err)
	}
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version, Watermark: 7}); err != nil {
		if !errors.Is(err, ErrAccessNotReady) {
			t.Fatal(err)
		}
	} else {
		t.Fatal("unbuilt profiles marked ready")
	}
	if _, err = s.BuildAccessProfiles(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version}, []string{actor}); err != nil {
		t.Fatal(err)
	}
	accessTestCheckpoint(t, s, AccessBuildToken{Revision: r.ID, Version: r.Version, Watermark: 7})
	if err = s.FinishPreparation(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version, Watermark: 7}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Revision(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ready" || got.Watermark != 7 {
		t.Fatal(got)
	}
	var active int
	if err = s.DB.QueryRow(`SELECT active_revision FROM acl_state`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("ready implicitly applied")
	}
}

func TestAccessReusedAccountDoesNotInheritIdentity(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	again, err := s.RegisterPrincipal(ctx, 1)
	if err != nil || again != actor {
		t.Fatal("mapping unstable", err)
	}
	if err = s.ForgetPrincipal(ctx, 1); err != nil {
		t.Fatal(err)
	}
	newID, err := s.RegisterPrincipal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if newID == actor {
		t.Fatal("reused account inherited ACL identity")
	}
	if _, err = s.SaveDraft(ctx, 0, 0, 0, newID, access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.DenyAll}}}}); err == nil {
		t.Fatal("deleted identity referenced")
	}
	if err = s.ForgetPrincipal(ctx, 0); err == nil {
		t.Fatal("guest mapping can be deleted")
	}
}

func TestAccessRevisionRejectsChangedBaseAndUnknownActor(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveDraft(ctx, 0, 0, 1, actor, access.Policy{}); !errors.Is(err, ErrAccessConflict) {
		t.Fatal(err)
	}
	if _, err := s.SaveDraft(ctx, 0, 0, 0, "unknown", access.Policy{}); err == nil {
		t.Fatal("unknown actor accepted")
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE acl_state SET active_revision=100`); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("obsolete draft started", err)
	}
}

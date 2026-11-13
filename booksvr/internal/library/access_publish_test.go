package library

import (
	"context"
	"errors"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessPublicationRequiresCompleteReviewedSnapshot(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.DenyAll}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, 0, actor); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("draft published", err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := AccessBuildToken{Revision: r.ID, Version: r.Version}
	if _, err = s.BuildAccessProfiles(ctx, token, []string{actor}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPreparation(ctx, token); !errors.Is(err, ErrAccessNotReady) {
		t.Fatal("unscanned projection marked ready", err)
	}
	accessTestCheckpoint(t, s, token)
	if err = s.FinishPreparation(ctx, token); err != nil {
		t.Fatal(err)
	}
	oldScope, err := s.ResolveAccess(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, 1, actor); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("wrong review accepted", err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, 0, actor); err != nil {
		t.Fatal(err)
	}
	if err = s.AccessCurrent(ctx, oldScope); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("old response stayed authorized", err)
	}
	scope, err := s.ResolveAccess(ctx, 1, false)
	if err != nil || scope.Unrestricted() {
		t.Fatal("deny all not applied", scope, err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, 0, actor); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("duplicate apply accepted", err)
	}
}

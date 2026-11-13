package library

import (
	"context"
	"talebook/booksvr/internal/access"
	"testing"
)

func TestAccessCleanupPreservesActiveAndHistory(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	token := AccessBuildToken{Revision: r.ID, Version: r.Version}
	profiles, err := s.BuildAccessProfiles(ctx, token, []string{actor})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WriteAccessBatch(ctx, token, profiles, []AccessBookSnapshot{{ID: 1, Files: []AccessFileSnapshot{{ID: "file", Format: "EPUB", Version: 1, Facts: access.FileFacts{Ready: true}}}}}); err != nil {
		t.Fatal(err)
	}
	if worked, e := s.CleanupAccessBatch(ctx); e != nil || worked {
		t.Fatal("removed preparing projection", worked, e)
	}
	if err = s.CancelPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	if worked, e := s.CleanupAccessBatch(ctx); e != nil || !worked {
		t.Fatal(worked, e)
	}
	for _, table := range []string{"acl_profile_book", "acl_profile_file"} {
		var n int
		if e := s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
			t.Fatal(table, n, e)
		}
	}
	if _, err = s.Revision(ctx, r.ID); err != nil {
		t.Fatal("policy history removed", err)
	}
	if worked, e := s.CleanupAccessBatch(ctx); e != nil || worked {
		t.Fatal("idle cleanup did work", worked, e)
	}
}

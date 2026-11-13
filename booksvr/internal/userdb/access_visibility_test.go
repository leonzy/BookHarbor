package userdb

import (
	"context"
	"testing"
)

func TestAccessVisibilityOutboxOnlyTracksRelevantChanges(t *testing.T) {
	db := extDB(t)
	if err := MigrateAccessVisibility(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scopeSeedItem(t, db, 10, "public", int64(1), 0, 0)
	if epoch, err := AccessVisibilityEpoch(ctx, db); err != nil || epoch != 0 {
		t.Fatal("public import queued visibility work", epoch, err)
	}
	if err := SetItemScope(db, 10, "private"); err != nil {
		t.Fatal(err)
	}
	ids, watermark, err := AccessVisibilityChanges(ctx, db, 0)
	if err != nil || len(ids) != 1 || ids[0] != 10 || watermark == 0 {
		t.Fatal(ids, watermark, err)
	}
	owners, epoch, err := AccessOwners(ctx, db, []int64{10, 20})
	if err != nil || epoch != watermark || len(owners) != 2 || !owners[0].Private || owners[0].AccountID != 1 || owners[1].Private {
		t.Fatal(owners, epoch, err)
	}
	if _, err = db.Exec(`UPDATE items SET count_visit=count_visit+1 WHERE book_id=10`); err != nil {
		t.Fatal(err)
	}
	if next, err := AccessVisibilityEpoch(ctx, db); err != nil || next != epoch {
		t.Fatal("reading statistics dirtied visibility", next, err)
	}
	if err = SetItemScope(db, 10, "public"); err != nil {
		t.Fatal(err)
	}
	ids, next, err := AccessVisibilityChanges(ctx, db, watermark)
	if err != nil || len(ids) != 1 || next <= watermark {
		t.Fatal("public transition lost", ids, next, err)
	}
}

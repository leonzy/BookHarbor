package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"talebook/booksvr/internal/access"
)

func TestAccessWorkerResumesAndCatchesConcurrentChanges(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	_, err := s.DB.Exec(`CREATE TABLE books(id INTEGER PRIMARY KEY,title TEXT,timestamp TEXT,path TEXT); INSERT INTO books VALUES(1,'one','','public'),(2,'two','','public'),(3,'three','','public')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateAccess(s.DB); err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{Rules: []access.Rule{{ID: "secret", Target: access.Target{Root: "root", Directory: "/secret", Recursive: true}, Mode: access.DenyAll}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, tx *sql.Tx, ids []int64) ([]AccessBookSnapshot, error) {
		var out []AccessBookSnapshot
		for _, id := range ids {
			b := AccessBookSnapshot{ID: id}
			var path string
			err := tx.QueryRowContext(ctx, `SELECT title,timestamp,path FROM books WHERE id=?`, id).Scan(&b.Title, &b.Timestamp, &path)
			if err != nil && err != sql.ErrNoRows {
				return nil, err
			}
			if err == nil {
				b.Files = []AccessFileSnapshot{{ID: fmt.Sprint("file-", id), Format: "EPUB", Size: 100, Version: 1, Facts: access.FileFacts{Root: "root", Directory: "/" + path, Ready: true}}}
			}
			out = append(out, b)
		}
		return out, nil
	}
	step := func() bool {
		t.Helper()
		// A new instance on every step simulates a process restart: no memory cursor.
		worked, e := (AccessWorker{Store: s, Read: read, BatchSize: 1}).Step(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		return worked
	}
	step() // initialize
	step() // first book
	_, err = s.DB.Exec(`UPDATE books SET path='secret' WHERE id=1; DELETE FROM books WHERE id=2; INSERT INTO books VALUES(4,'four','','public')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if !step() {
			break
		}
		if i == 19 {
			t.Fatal("worker did not become idle")
		}
	}
	r, err = s.Revision(ctx, r.ID)
	if err != nil || r.Status != "ready" {
		t.Fatal(r, err)
	}
	var count int
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_profile_book WHERE revision=? AND book_id IN (3,4)`, r.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_profile_book WHERE revision=? AND book_id IN (1,2)`, r.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("moved/deleted book resurrected", count, err)
	}
	var before, after int
	s.DB.QueryRow(`SELECT total_changes()`).Scan(&before)
	if step() {
		t.Fatal("idle worker performed work")
	}
	s.DB.QueryRow(`SELECT total_changes()`).Scan(&after)
	if before != after {
		t.Fatal("idle worker wrote to database", before, after)
	}
	// A new mutation invalidates a ready preview until the bounded delta pass
	// and its review watermark have both caught up.
	if _, err = s.DB.Exec(`UPDATE books SET path='secret' WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewAccess(ctx, r.ID, actor); !errors.Is(err, ErrAccessNotReady) {
		t.Fatal("stale preview accepted", err)
	}
	for i := 0; i < 8; i++ {
		if !step() {
			break
		}
		if i == 7 {
			t.Fatal("ready delta did not settle")
		}
	}
	r, err = s.Revision(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, r.Watermark, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO books VALUES(5,'five','','public')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if !step() {
			break
		}
		if i == 7 {
			t.Fatal("active delta did not settle")
		}
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM acl_profile_book WHERE book_id=5`).Scan(&count); err != nil || count != 1 {
		t.Fatal("active import did not appear", count, err)
	}
}

func TestAccessWorkerLeaseFencesExpiredWriter(t *testing.T) {
	s, actor := accessTestStore(t)
	ctx := context.Background()
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO acl_worker_lease VALUES(1,'replacement',?)`, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuildAccessProfiles(ctx, AccessBuildToken{Revision: r.ID, Version: r.Version, Lease: "expired"}, []string{actor}); !errors.Is(err, ErrAccessConflict) {
		t.Fatal("expired worker accepted", err)
	}
	worked, err := (AccessWorker{Store: s}).Step(ctx, r.ID)
	if err != nil || worked {
		t.Fatal("second worker entered held lease", worked, err)
	}
}

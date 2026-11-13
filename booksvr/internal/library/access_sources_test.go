package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"talebook/booksvr/internal/access"
)

func TestAccessSourceIdentityAndExactFileResolution(t *testing.T) {
	db := newQueryDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAccess(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO acl_book_owner(book_id,principal,private) SELECT id,'',0 FROM books`); err != nil {
		t.Fatal(err)
	}
	s := AccessStore{DB: db}
	ctx := context.Background()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, ".talebook-source-health"), []byte("mount-token"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := s.ObserveAccessRoot(ctx, base, "mount-token", true)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.RegisterPrincipal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,'EPUB',?,100,1),(1,'MOBI',?,200,1)`, filepath.Join(base, "public", "one.epub"), filepath.Join(base, "secret", "one.mobi")); err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveDraft(ctx, 0, 0, 0, actor, access.Policy{Principals: []access.Principal{{ID: actor, Range: access.Ceiling{Mode: access.Selected, Targets: []access.Target{{Root: root.ID, Directory: "/public", Recursive: true}}}}}, Rules: []access.Rule{{ID: "restricted", Target: access.Target{Root: root.ID, Directory: "/secret", Recursive: true}, Mode: access.DenyAll}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginPreparation(ctx, r.ID, r.Version); err != nil {
		t.Fatal(err)
	}
	w := AccessWorker{Store: s, Read: AccessSourceReader(base), ReadWrites: true, BatchSize: 2}
	for i := 0; i < 30; i++ {
		done, e := w.Step(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !done {
			break
		}
		if i == 29 {
			t.Fatal("worker did not settle")
		}
	}
	r, err = s.Revision(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishAccess(ctx, r.ID, r.Version, r.Watermark, actor); err != nil {
		t.Fatal(err)
	}
	scope, err := s.ResolveAccess(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.AccessResolveFile(ctx, scope, 1, "epub", base)
	if err != nil || file.Size != 100 {
		t.Fatal(file, err)
	}
	if allowed, e := s.AccessCanShareCover(ctx, scope, 1); e != nil || allowed {
		t.Fatal("unknown cover provenance exposed hidden source", allowed, e)
	}
	if _, err = s.AccessResolveFile(ctx, scope, 1, "mobi", base); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("hidden format resolved", err)
	}
	if _, err = db.Exec(`UPDATE book_formats SET path=? WHERE book_id=1 AND format='MOBI'`, filepath.Join(base, "public", "moved.mobi")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		done, e := w.Step(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !done {
			break
		}
	}
	scope, err = s.ResolveAccess(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AccessResolveFile(ctx, scope, 1, "mobi", base); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("move widened resource rule", err)
	}
	next, err := s.SaveDraft(ctx, 0, 0, r.ID, actor, access.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Policy.OriginRules) == 0 {
		t.Fatal("new draft erased inherited restrictions")
	}
	if _, err = db.Exec(`UPDATE book_formats SET path=? WHERE book_id=1 AND format='EPUB'`, filepath.Join(base, "secret", "moved.epub")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AccessResolveFile(ctx, scope, 1, "epub", base); err == nil {
		t.Fatal("moved file reused old grant")
	}
	var oldID string
	var rowID int64
	if err = db.QueryRow(`SELECT f.id,i.id FROM book_formats f JOIN acl_source_identity i ON i.source_key='f:'||f.id WHERE f.book_id=1 AND f.format='EPUB'`).Scan(&rowID, &oldID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM book_formats WHERE id=?`, rowID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(?,1,'EPUB',?,100,1)`, rowID, filepath.Join(base, "public", "other.epub")); err != nil {
		t.Fatal(err)
	}
	var newID string
	if err = db.QueryRow(`SELECT id FROM acl_source_identity WHERE source_key='f:'||?`, rowID).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatal("reused database row inherited old authority")
	}
	changed, err := s.ObserveAccessRoot(ctx, base, "different-mount", true)
	if err != nil || changed.ID != root.ID || changed.Ready {
		t.Fatal("changed mount trusted", changed, err)
	}
	if _, err = s.AccessResolveFile(ctx, scope, 1, "epub", base); err == nil {
		t.Fatal("offline mount resolved")
	}
	for i := 0; i < 10; i++ {
		worked, e := s.RefreshAccessRootBatch(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			break
		}
	}
	for i := 0; i < 10; i++ {
		worked, e := w.Step(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			break
		}
	}
	if _, err = s.ObserveAccessRoot(ctx, base, "mount-token", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		worked, e := s.RefreshAccessRootBatch(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			break
		}
	}
	for i := 0; i < 10; i++ {
		worked, e := w.Step(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			break
		}
	}
	scope, err = s.ResolveAccess(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AccessResolveFile(ctx, scope, 1, "epub", base); err != nil {
		t.Fatal("mount recovery did not restore visible book", err)
	}
	if worked, e := s.RefreshAccessRootBatch(ctx); e != nil || worked {
		t.Fatal("idle root replay performed work", worked, e)
	}
}

package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceFullSnapshotDoesNotRevivePrunedBook(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-state", true: "existing-state"}[seeded], func(t *testing.T) {
			db := newQueryDB(t)
			if e := Migrate(db); e != nil {
				t.Fatal(e)
			}
			if e := EnsureImportSizeAudit(db); e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			file := filepath.Join(root, "small.txt")
			os.WriteFile(file, []byte("small"), 0600)
			db.Exec(`INSERT INTO books(id,title) VALUES(500,'small')`)
			db.Exec(`INSERT INTO book_formats(id,book_id,format,path,size,mtime) VALUES(500,500,'TXT',?,5,0)`, file)
			db.Exec(`INSERT INTO data(book,format,name) VALUES(500,'TXT','small')`)
			if seeded {
				if _, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{FullDiscovery: true}); e != nil {
					t.Fatal(e)
				}
			}
			db.Exec(`INSERT INTO import_size_audit(id,generation,minimum,max_id,total,state,updated_at) VALUES(1,'parallel',10240,500,1,'urgent',0)`)
			removed := false
			_, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{FullDiscovery: true, DirectoryVisited: func(string) error {
				if removed {
					return nil
				}
				info, e := os.Stat(file)
				if e != nil {
					return e
				}
				removed, e = PruneImportFormat(context.Background(), db, "parallel", 500, file, file, info, 10240, false)
				return e
			}})
			if e != nil || !removed {
				t.Fatal(removed, e)
			}
			var n int
			db.QueryRow(`SELECT count(*) FROM books WHERE id=500`).Scan(&n)
			if n != 0 {
				t.Fatal("book revived")
			}
			db.QueryRow(`SELECT count(*) FROM source_sync_formats WHERE format_id=500`).Scan(&n)
			if n != 0 {
				t.Fatal("stale availability revived")
			}
			if _, e = os.Stat(file); e != nil {
				t.Fatal("source removed", e)
			}
		})
	}
}

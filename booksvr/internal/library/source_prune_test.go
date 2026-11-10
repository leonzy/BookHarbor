package library

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"talebook/booksvr/internal/sourcefile"
)

func TestMissingSourcePruneVersionsAndGuards(t *testing.T) {
	for _, mode := range []string{"last", "another-format", "same-format", "restored", "marker", "moved", "delay", "symlink", "zip", "commit-offline"} {
		t.Run(mode, func(t *testing.T) {
			db := newQueryDB(t)
			db.SetMaxOpenConns(1)
			if e := Migrate(db); e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			run := func() {
				if _, e := SyncSourceFiles(context.Background(), db, []string{root}, ""); e != nil {
					t.Fatal(e)
				}
			}
			file := filepath.Join(root, "book.txt")
			sourceWrite(t, file)
			if runtime.GOOS == "windows" {
				time.Sleep(20 * time.Millisecond)
			}
			sourceWrite(t, filepath.Join(root, "sentinel.txt"))
			os.WriteFile(filepath.Join(root, "sentinel.txt"), []byte("different sentinel"), 0600)
			path := file
			if mode == "zip" {
				path = sourcefile.Encode(sourcefile.Reference{Archive: file, Name: "book.txt", Size: 4, Chain: []string{"book.txt"}, Hash: "sha256:" + strings.Repeat("0", 64)})
			}
			sourceRegister(t, db, 10, path, "TXT")
			if mode == "moved" {
				h, e := sourceHash(context.Background(), file)
				if e != nil {
					t.Fatal(e)
				}
				if e = SeedSourceHashes(db, map[string]string{path: h}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := db.Exec(`INSERT INTO data(book,format,name) VALUES(10,'TXT','book')`); e != nil {
				t.Fatal(e)
			}
			keep := mode == "another-format" || mode == "same-format"
			if keep {
				if runtime.GOOS == "windows" {
					time.Sleep(20 * time.Millisecond)
				}
				other := filepath.Join(root, "other.epub")
				sourceWrite(t, other)
				os.WriteFile(other, []byte("another edition"), 0600)
				format := "EPUB"
				if mode == "same-format" {
					format = "txt"
				}
				sourceRegister(t, db, 10, other, format)
				if format == "EPUB" {
					db.Exec(`INSERT INTO data(book,format,name) VALUES(10,'EPUB','other')`)
				}
			}
			run()
			if e := os.Remove(file); e != nil {
				t.Fatal(e)
			}
			run()
			if mode == "moved" {
				target := filepath.Join(root, "renamed.txt")
				sourceWrite(t, target)
				run()
			}
			if mode == "restored" {
				sourceWrite(t, file)
			}
			if mode == "marker" {
				if e := os.WriteFile(filepath.Join(root, ".talebook-source-health"), []byte("wrong"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "symlink" {
				if e := os.Symlink(filepath.Join(root, "gone-target"), file); e != nil {
					t.Skip(e)
				}
			}
			var id int64
			if e := db.QueryRow(`SELECT id FROM book_formats WHERE book_id=10 ORDER BY id LIMIT 1`).Scan(&id); e != nil {
				t.Fatal(e)
			}
			candidate := MissingSourceCandidate{ID: id, Path: path}
			now := time.Now().Add(2 * time.Minute)
			if mode == "delay" {
				now = time.Now()
			}
			var allow []func() bool
			calls := 0
			if mode == "commit-offline" {
				allow = []func() bool{func() bool { calls++; return calls < 3 }}
			}
			removed, e := PruneMissingSource(context.Background(), db, []string{root}, "", candidate, now, allow...)
			if e != nil {
				t.Fatal(e)
			}
			want := mode == "last" || keep || mode == "zip"
			if removed != want {
				t.Fatalf("removed=%v want=%v", removed, want)
			}
			var count int
			db.QueryRow(`SELECT count(*) FROM books WHERE id=10`).Scan(&count)
			if want && !keep && count != 0 || (!want || keep) && count != 1 {
				t.Fatal("book retention", count)
			}
			if keep {
				var slots int
				db.QueryRow(`SELECT count(*) FROM data WHERE book=10`).Scan(&slots)
				if slots != 1 {
					t.Fatal("remaining slot", slots)
				}
			}
			if removed {
				again, e := PruneMissingSource(context.Background(), db, []string{root}, "", candidate, now)
				if e != nil || again {
					t.Fatal("not idempotent", again, e)
				}
			}
			if _, e := os.Stat(filepath.Join(root, "sentinel.txt")); e != nil {
				t.Fatal("source changed", e)
			}
		})
	}
}

func TestMissingSourcePruneBatchRollsBackLateProtection(t *testing.T) {
	for _, protect := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "late-protection"}[protect], func(t *testing.T) {
			db := newQueryDB(t)
			db.SetMaxOpenConns(1)
			if e := Migrate(db); e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			for i, name := range []string{"one.txt", "two.txt"} {
				file := filepath.Join(root, name)
				sourceWrite(t, file)
				sourceRegister(t, db, 600+i, file, "TXT")
				if runtime.GOOS == "windows" {
					time.Sleep(20 * time.Millisecond)
				}
			}
			run := func() {
				if _, e := SyncSourceFiles(context.Background(), db, []string{root}, ""); e != nil {
					t.Fatal(e)
				}
			}
			run()
			os.Remove(filepath.Join(root, "one.txt"))
			os.Remove(filepath.Join(root, "two.txt"))
			run()
			now := time.Now().Add(2 * time.Minute)
			candidates, e := MissingSourceCandidates(context.Background(), db, 0, now, 5)
			if e != nil || len(candidates) != 2 {
				t.Fatal(candidates, e)
			}
			calls := 0
			allow := func() bool { calls++; return !protect || calls < 8 }
			n, e := PruneMissingSources(context.Background(), db, []string{root}, "", candidates, now, allow)
			if e != nil {
				t.Fatal(e)
			}
			want := 2
			if protect {
				want = 0
			}
			if n != want {
				t.Fatal(n, want)
			}
			var remaining, outbox int
			db.QueryRow(`SELECT count(*) FROM books WHERE id IN (600,601)`).Scan(&remaining)
			db.QueryRow(`SELECT count(*) FROM source_prune_removed`).Scan(&outbox)
			if remaining != 2-want || outbox != want {
				t.Fatal("partial batch persisted", remaining, outbox)
			}
		})
	}
}

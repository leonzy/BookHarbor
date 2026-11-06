package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Models an ordinary unchanged baseline: the previous implementation indexed
// every inode/size, even though there were no missing sources to relocate.
func BenchmarkSourceRelocationIndexesUnchanged(b *testing.B) {
	files := map[string]sourceDiskFile{}
	tracked := make([]sourceTracked, 0, 100000)
	for i := 0; i < 100000; i++ {
		p := fmt.Sprintf("/books/long-source-directory/%08d.epub", i)
		key := fmt.Sprintf("fs:%d", i)
		files[p] = sourceDiskFile{path: p, key: key, size: int64(i + 1)}
		tracked = append(tracked, sourceTracked{physical: p, key: key, size: int64(i + 1)})
	}
	b.Run("previous", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			byKey := map[string][]sourceDiskFile{}
			bySize := map[int64][]sourceDiskFile{}
			for _, f := range files {
				byKey[f.key] = append(byKey[f.key], f)
				bySize[f.size] = append(bySize[f.size], f)
			}
			runtime.KeepAlive(byKey)
			runtime.KeepAlive(bySize)
		}
	})
	b.Run("needed_only", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			keys, sizes := sourceRelocationIndexes(tracked, files, func(string) bool { return true })
			runtime.KeepAlive(keys)
			runtime.KeepAlive(sizes)
		}
	})
}

func TestSourceSyncOmitFilePathsKeepsDiscoveryAndDeletion(t *testing.T) {
	db, root, run := sourceFixture(t)
	path := filepath.Join(root, "book.txt")
	sourceWrite(t, path)
	sourceRegister(t, db, 1, path, "TXT")
	run()
	result, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{FullDiscovery: true, OmitFilePaths: true})
	if e != nil || result.Checked != 1 || len(result.Files) != 0 || len(result.Directories) == 0 {
		t.Fatal(result, e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	result, e = SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{FullDiscovery: true, OmitFilePaths: true})
	if e != nil || result.Missing != 1 {
		t.Fatal(result, e)
	}
	sourceMissing(t, db, 1, true)
}

func TestSourceSyncOmitFilePathsPreservesLimitedMoveDiscovery(t *testing.T) {
	db, root, run := sourceFixture(t)
	before := filepath.Join(root, "before.txt")
	sourceWrite(t, before)
	sourceRegister(t, db, 1, before, "TXT")
	run()
	after := filepath.Join(root, "after.txt")
	if e := os.Rename(before, after); e != nil {
		t.Fatal(e)
	}
	result, e := SyncSourceFiles(context.Background(), db, []string{root}, "", SourceSyncOptions{OmitFilePaths: true})
	if e != nil || result.Moved != 1 || result.Missing != 0 || len(result.Files) != 0 {
		t.Fatal(result, e)
	}
}

func TestSourceInventoryCachesOnlyWhenUsed(t *testing.T) {
	tracked := []sourceTracked{{physical: "/root/a", key: "1:a", size: 10}, {physical: "/root/a", missing: 1}, {physical: "/root/b", key: "1:b", size: 20}}
	known, cached, moved := prepareSourceFiles(tracked, SourceSyncOptions{FullDiscovery: true}, true)
	if len(known) != 2 || known["/root/a"]&sourcePreviouslyPresent == 0 || cached != nil || moved != nil {
		t.Fatal(known, cached, moved)
	}
	known, cached, moved = prepareSourceFiles(tracked, SourceSyncOptions{Moves: []SourceMove{{From: "/root/a", To: "/root/renamed"}}}, true)
	if len(cached) != 2 || len(moved) != 1 || moved["/root/renamed"].path != "/root/renamed" {
		t.Fatal(known, cached, moved)
	}
	_, cached, moved = prepareSourceFiles(tracked, SourceSyncOptions{FullDiscovery: true, Moves: []SourceMove{{From: "/root", To: "/new"}}}, true)
	if cached != nil || len(moved) != 2 {
		t.Fatal(cached, moved)
	}
}

func TestSourceRelocationIndexPreservesAmbiguityWithoutIndexingWholeSnapshot(t *testing.T) {
	files := map[string]sourceDiskFile{}
	tracked := []sourceTracked{}
	for i := 0; i < 1000; i++ {
		p := fmt.Sprintf("/root/live%d", i)
		f := sourceDiskFile{path: p, key: p, size: int64(100 + i)}
		files[p] = f
		tracked = append(tracked, sourceTracked{physical: p, key: p, size: f.size})
	}
	byKey, bySize := sourceRelocationIndexes(tracked, files, func(string) bool { return true })
	if len(byKey) != 0 || len(bySize) != 0 {
		t.Fatal("unchanged baseline built relocation maps")
	}
	files["/root/first"] = sourceDiskFile{path: "/root/first", key: "wanted", size: 7}
	files["/root/claimed"] = sourceDiskFile{path: "/root/claimed", key: "wanted", size: 7}
	tracked = append(tracked, sourceTracked{physical: "/root/gone", key: "wanted", size: 7, hash: "sha256:abc"}, sourceTracked{physical: "/root/claimed", key: "wanted", size: 7})
	byKey, bySize = sourceRelocationIndexes(tracked, files, func(string) bool { return true })
	if len(byKey) != 1 || len(byKey["wanted"]) != 2 || len(bySize) != 1 || len(bySize[7]) != 2 {
		t.Fatal(byKey, bySize)
	}
	byKey, bySize = sourceRelocationIndexes(tracked, files, func(string) bool { return false })
	if len(byKey) != 0 || len(bySize) != 0 {
		t.Fatal("protected root indexed as relocation origin")
	}
}

func TestSourceWithinKeepsRelSemantics(t *testing.T) {
	for _, v := range [][2]string{{"/books/a", "/books"}, {"/books2/a", "/books"}, {"/books/../outside", "/books"}, {"/books", "/books"}, {"/a", "/"}, {"/books//a/./b", "/books/"}, {"relative/a", "relative"}, {"/BOOKS/a", "/books"}} {
		rel, e := filepath.Rel(v[1], v[0])
		want := e == nil && rel != ".." && !(len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator))
		if got := sourceWithin(v[0], v[1]); got != want {
			t.Fatalf("%q in %q = %v; want %v", v[0], v[1], got, want)
		}
	}
}

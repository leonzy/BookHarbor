package library

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAccessDirectoryUsesLongestRootAndRejectsSibling(t *testing.T) {
	s, _ := accessTestStore(t)
	ctx := context.Background()
	base := t.TempDir()
	root, err := s.ObserveAccessRoot(ctx, base, "root", true)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.ObserveAccessRoot(ctx, filepath.Join(base, "nested"), "child", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE TABLE book_directory_nodes(path TEXT PRIMARY KEY,parent TEXT NOT NULL,name TEXT NOT NULL); INSERT INTO book_directory_nodes(rowid,path,parent,name) VALUES(1,?,'','root'),(2,?,'','nested'),(3,?,'','sibling')`, base, filepath.Join(base, "nested", ".hidden"), base+"-other"); err != nil {
		t.Fatal(err)
	}
	target, err := s.AccessDirectoryTarget(ctx, 1, false)
	if err != nil || target.Root != root.ID || target.Directory != "/" || target.Recursive {
		t.Fatal(target, err)
	}
	target, err = s.AccessDirectoryTarget(ctx, 2, true)
	if err != nil || target.Root != nested.ID || target.Directory != "/.hidden" || !target.Recursive {
		t.Fatal(target, err)
	}
	if _, err = s.AccessDirectoryTarget(ctx, 3, true); err == nil {
		t.Fatal("sibling path accepted")
	}
}

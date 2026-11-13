package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccessPhysicalPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := accessCheckPhysicalPath(root, filepath.Join(root, "absent", "book.epub")); err != nil {
		t.Fatal(err)
	}
	if err := accessCheckPhysicalPath(root, filepath.Join(outside, "book.epub")); err == nil {
		t.Fatal("outside root accepted")
	}
	link := filepath.Join(root, "public")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if err := accessCheckPhysicalPath(root, filepath.Join(link, "book.epub")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

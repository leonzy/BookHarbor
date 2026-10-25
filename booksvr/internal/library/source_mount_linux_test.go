package library

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSourceMountIdentityIgnoresContainerParent(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("1 0 0:11 / / rw - overlay overlay rw\n2 1 8:32 /physical /books rw - f2fs /dev/sdc rw\n")
	next := []byte("44 0 0:99 / / rw - overlay overlay rw\n55 44 8:32 /physical /books rw - f2fs /dev/sdc rw\n")
	if stableSourceMountIdentity("/books", info, first, func(p string) string { return p }) != stableSourceMountIdentity("/books", info, next, func(p string) string { return p }) {
		t.Fatal("container recreation changed source identity")
	}
	nested := append(append([]byte{}, next...), []byte("56 55 8:48 /nested /books/sub rw - ext4 /dev/sdd rw\n")...)
	if stableSourceMountIdentity("/books", info, next, func(p string) string { return p }) == stableSourceMountIdentity("/books", info, nested, func(p string) string { return p }) {
		t.Fatal("nested mount ignored")
	}
}
func TestSourceLegacyIdentityRequiresExplicitBaseline(t *testing.T) {
	db, root, run := sourceFixture(t)
	file := filepath.Join(root, "book.txt")
	sourceWrite(t, file)
	sourceRegister(t, db, 1, file, "TXT")
	run()
	info, _ := os.Stat(root)
	if _, err := db.Exec(`UPDATE source_sync_roots SET identity=?`, sourceFileIdentity(info)+":legacy-overlay"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if result := run(); result.Roots[root] == "ok" || result.Missing != 0 {
		t.Fatal(result)
	}
	// A valid root marker does not prove that a former nested mount survived.
	// Rebuild only after an administrator has inspected a non-empty source.
	sourceWrite(t, filepath.Join(root, "other.txt"))
	if _, err := RebaselineSourceRoot(context.Background(), db, root, "wrong"); err == nil {
		t.Fatal("unchecked rebaseline accepted")
	}
	inspection, err := InspectSourceRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RebaselineSourceRoot(context.Background(), db, root, inspection.Identity); err != nil {
		t.Fatal(err)
	}
	if result := run(); result.Roots[root] != "ok" || result.Missing != 1 {
		t.Fatal(result)
	}
}

func TestSourceMountIdentityStableAcrossDeviceRenumber(t *testing.T) {
	info, _ := os.Stat(t.TempDir())
	first := []byte("2 1 8:32 /physical /books rw - f2fs /dev/sdc rw\n3 2 8:48 /nested /books/sub rw - ext4 /dev/sdd rw\n")
	next := []byte("22 11 8:48 /physical /books rw - f2fs /dev/sdd rw\n33 22 8:64 /nested /books/sub rw - ext4 /dev/sde rw\n")
	fs := func(path string) string { return "stable-fs-" + path }
	a := stableSourceMountIdentity("/books", info, first, fs)
	b := stableSourceMountIdentity("/books", info, next, fs)
	if a != b {
		t.Fatal("volatile device number retained")
	}
	different := func(path string) string { return "replacement-" + path }
	if a == stableSourceMountIdentity("/books", info, next, different) {
		t.Fatal("replacement filesystem trusted")
	}
	lost := []byte("22 11 8:48 /physical /books rw - f2fs /dev/sdd rw\n")
	if a == stableSourceMountIdentity("/books", info, lost, fs) {
		t.Fatal("lost nested mount trusted")
	}
}
func TestSourceDeviceRenumberDoesNotRehashUnchangedFile(t *testing.T) {
	db, root, run := sourceFixture(t)
	p := filepath.Join(root, "book.txt")
	sourceWrite(t, p)
	sourceRegister(t, db, 1, p, "TXT")
	run()
	// A sentinel hash must survive a device-number-only change. Reading and
	// recalculating every source at boot would overwrite it with the body hash.
	db.Exec(`UPDATE book_formats SET hash='sha256:sentinel'`)
	var identity string
	db.QueryRow(`SELECT identity FROM source_sync_formats LIMIT 1`).Scan(&identity)
	suffix := identity[strings.LastIndex(identity, ":")+1:]
	db.Exec(`UPDATE source_sync_formats SET identity=?`, "999999:"+suffix)
	run()
	var hash string
	db.QueryRow(`SELECT hash FROM book_formats LIMIT 1`).Scan(&hash)
	if hash != "sha256:sentinel" {
		t.Fatal("unchanged file rehashed", hash)
	}
}

type renumberedSourceInfo struct {
	os.FileInfo
	dev uint64
}

func (i renumberedSourceInfo) Sys() any {
	st := *i.FileInfo.Sys().(*syscall.Stat_t)
	st.Dev = i.dev
	return &st
}
func TestSourceRootIgnoresStatDeviceRenumber(t *testing.T) {
	info, _ := os.Stat(t.TempDir())
	data := []byte("2 1 8:32 /physical /books rw - f2fs /dev/sdc rw\n")
	fs := func(string) string { return "stable-filesystem" }
	a := stableSourceMountIdentity("/books", renumberedSourceInfo{info, 2080}, data, fs)
	b := stableSourceMountIdentity("/books", renumberedSourceInfo{info, 2096}, data, fs)
	if a != b {
		t.Fatal("st_dev included in stable root identity")
	}
}

func TestSourceF2FSUUIDIgnoresDeviceBasedStatfsID(t *testing.T) {
	uuid, _ := hex.DecodeString("0123456789abcdef0123456789abcdef")
	a := stableFilesystemIdentity(0xf2f52010, [2]int32{2080, 0}, 2080, uuid)
	b := stableFilesystemIdentity(0xf2f52010, [2]int32{2096, 0}, 2096, uuid)
	if a == "" || a != b || !strings.Contains(a, "uuid:01234567") {
		t.Fatal(a, b)
	}
	if stableFilesystemIdentity(0xf2f52010, [2]int32{2096, 0}, 2096, nil) != "" {
		t.Fatal("volatile F2FS f_fsid trusted")
	}
	uuid[0] ^= 0xff
	if a == stableFilesystemIdentity(0xf2f52010, [2]int32{2096, 0}, 2096, uuid) {
		t.Fatal("different volume UUID trusted")
	}
}
func TestSourceFilesystemUUIDOnRealMount(t *testing.T) {
	root := os.Getenv("BOOKHARBOR_TEST_SOURCE_ROOT")
	if root == "" {
		t.Skip("opt-in read-only real filesystem integration")
	}
	expected := strings.ReplaceAll(os.Getenv("BOOKHARBOR_TEST_SOURCE_UUID"), "-", "")
	identity := sourceFilesystemIdentity(root)
	if expected == "" || !strings.Contains(identity, "uuid:"+expected) {
		t.Fatal("actual UUID not used", identity)
	}
	t.Log(identity)
}

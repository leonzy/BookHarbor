package library

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedCoversIsolateExternalBooksAndPreserveExisting(t *testing.T) {
	db := newQueryDB(t)
	root, source := t.TempDir(), t.TempDir()
	var ids []int64
	for _, title := range []string{"a", "b"} {
		id, err := ImportBook(db, BookMeta{Title: title, Path: source})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for i, id := range ids {
		img := image.NewRGBA(image.Rect(0, 0, 1600, 1400))
		img.Set(0, 0, color.RGBA{R: uint8(i * 200), A: 255})
		var b bytes.Buffer
		jpeg.Encode(&b, img, nil)
		created, err := StoreCover(db, root, id, b.Bytes())
		if err != nil || !created {
			t.Fatalf("%v %v", created, err)
		}
		got, err := Cover(db, id, root)
		if err != nil {
			t.Fatal(err)
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(got))
		if err != nil || config.Width > 800 || config.Height > 800 || len(got) > 512<<10 {
			t.Fatalf("%+v %v", config, err)
		}
		if changed, err := StoreCover(db, root, id, b.Bytes()); err != nil || changed {
			t.Fatalf("overwrote existing: %v %v", changed, err)
		}
		if ts, err := CoverLastModified(db, id, root); err != nil || ts == 0 {
			t.Fatalf("mtime: %d %v", ts, err)
		}
	}
	entries, _ := os.ReadDir(source)
	if len(entries) != 0 {
		t.Fatal("wrote source directory")
	}
	entries, _ = os.ReadDir(filepath.Join(root, ".talebook-covers"))
	if len(entries) != 2 {
		t.Fatal("temporary files leaked")
	}
	if _, err := StoreCover(db, root, ids[0], []byte("not image")); err == nil {
		t.Fatal("accepted invalid image")
	}
	if _, err := Cover(db, 9999, root); err != nil {
		t.Fatal(err)
	}
}

func TestFormatSizesAreBatchedAndDuplicateFormatsCountOnce(t *testing.T) {
	db := newQueryDB(t)
	db.Exec(`INSERT INTO data(book,format,uncompressed_size,name) VALUES(1,'mobi',4321,'x'),(1,'MOBI',4321,'duplicate'),(1,'PDF',NULL,'unknown'),(1,'TXT',0,'empty')`)
	books, err := GetBooksData(db, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if books[0].FormatSizes["MOBI"] != 4321 || books[0].FormatSizes["EPUB"] != 1000 {
		t.Fatalf("%+v", books[0])
	}
	if _, ok := books[0].FormatSizes["PDF"]; ok {
		t.Fatal("unknown size became zero")
	}
	if value, ok := books[0].FormatSizes["TXT"]; !ok || value != 0 {
		t.Fatal("zero-byte file omitted")
	}

	seen := 0
	for _, f := range books[0].Formats {
		if f == "MOBI" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatal(books[0].Formats)
	}
}

func TestDeletingExternalIndexRemovesOnlyManagedCover(t *testing.T) {
	db := newQueryDB(t)
	root, source := t.TempDir(), t.TempDir()
	id, err := ImportBook(db, BookMeta{Title: "external", Path: source})
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(source, "book.epub")
	os.WriteFile(original, []byte("original"), 0644)
	os.MkdirAll(filepath.Dir(ManagedCoverPath(root, id)), 0755)
	os.WriteFile(ManagedCoverPath(root, id), []byte("managed image"), 0644)
	if err := DeleteBookToTrash(db, id, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ManagedCoverPath(root, id)); !os.IsNotExist(err) {
		t.Fatal("orphan cover")
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "original" {
		t.Fatal("source changed")
	}
}

func TestDeletingManagedBookMovesArtworkToTrash(t *testing.T) {
	db := newQueryDB(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "刘慈欣", "三体 (1)"), 0755)
	os.MkdirAll(filepath.Dir(ManagedCoverPath(root, 1)), 0755)
	os.WriteFile(ManagedCoverPath(root, 1), []byte("artwork"), 0644)
	if err := DeleteBookToTrash(db, 1, root); err != nil {
		t.Fatal(err)
	}
	dst, err := trashBookDir(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, trashCoverFile)); err != nil || string(data) != "artwork" {
		t.Fatalf("trash lost artwork: %v", err)
	}
}

// Synthetic solid-color fixtures generated for this test, not book artwork.
func TestManagedCoverAcceptsWebPVariants(t *testing.T) {
	for _, fixture := range []string{
		"UklGRh4AAABXRUJQVlA4TBEAAAAvCAADAAfQuQZVtf+BiOh/AAA=",
		"UklGRjgAAABXRUJQVlA4ICwAAADwAQCdASoJAA0AAUAmJaACdLoB+AAETAAA/uveL/yU1/jblq/687nOmwAAAA==",
	} {
		db := newQueryDB(t)
		root := t.TempDir()
		data, err := base64.StdEncoding.DecodeString(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := ReplaceCover(db, root, 1, data); err != nil || !ok {
			t.Fatal(ok, err)
		}
		got, err := os.ReadFile(ManagedCoverPath(root, 1))
		if err != nil {
			t.Fatal(err)
		}
		config, kind, err := image.DecodeConfig(bytes.NewReader(got))
		if err != nil || kind != "jpeg" || config.Width != 9 || config.Height != 13 {
			t.Fatal(config, kind, err)
		}
		if _, err := ReplaceCover(db, root, 1, data[:15]); err == nil {
			t.Fatal("truncated WebP accepted")
		}
		after, _ := os.ReadFile(ManagedCoverPath(root, 1))
		if !bytes.Equal(got, after) {
			t.Fatal("invalid replacement damaged cover")
		}
	}
}

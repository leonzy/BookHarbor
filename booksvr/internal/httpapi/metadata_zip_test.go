package httpapi

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"talebook/booksvr/internal/booktools"
	"talebook/booksvr/internal/sourcefile"
)

func TestMetadataZIPReadOnlySources(t *testing.T) {
	for _, mode := range []string{"readable", "missing", "changed", "insufficient", "locked"} {
		t.Run(mode, func(t *testing.T) {
			w, _, _ := referenceFixture(t)
			worker := testMetadataWorker(t, w)
			body := []byte("作者：作者甲\nISBN：9780141439518\n出版社：人民文学出版社\n")
			if mode == "insufficient" {
				body = []byte("《江湖听风录》\n第六十章\n正文。\n")
			}
			archive := filepath.Join(t.TempDir(), "source.zip")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			z := zip.NewWriter(f)
			entry, err := z.Create("novel.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = entry.Write(body); err != nil {
				t.Fatal(err)
			}
			if err = z.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			fp, err := sourcefile.Fingerprint(archive)
			if err != nil {
				t.Fatal(err)
			}
			ref := sourcefile.Encode(sourcefile.Reference{Archive: archive, Fingerprint: fp, Chain: []string{base64.StdEncoding.EncodeToString([]byte("novel.txt"))}, Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Name: "novel.txt", Size: int64(len(body))})
			if _, err = w.libraryDB.Exec(`UPDATE book_formats SET path=?,size=? WHERE book_id=1`, ref, len(body)); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err = os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			} else if mode == "changed" {
				if err = os.Chtimes(archive, time.Now(), time.Now().Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			} else if mode == "locked" {
				if err = w.updateBookMeta(1, "人工书名", map[string]any{"authors": []string{"人工作者"}, "isbn": ""}); err != nil {
					t.Fatal(err)
				}
			}
			if err = worker.local(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			status := jobStatus(t, worker, 1)
			b, err := bookGetOne(w.Config(""), 1)
			if err != nil || b == nil {
				t.Fatal(err)
			}
			switch mode {
			case "readable":
				if status == "file_retry" || b.ISBN != "9780141439518" || b.Authors[0] != "作者甲" || b.Publisher != "人民文学出版社" {
					t.Fatal(status, b)
				}
			case "missing", "changed":
				if status != "file_retry" || b.ISBN != "" {
					t.Fatal(status, b)
				}
			case "insufficient":
				if status != "insufficient" || b.ISBN != "" {
					t.Fatal(status, b)
				}
			case "locked":
				if b.Title != "人工书名" || b.ISBN != "" || b.Authors[0] != "人工作者" {
					t.Fatal("manual edit changed", b)
				}
			}
			if mode != "missing" {
				after, e := os.ReadFile(archive)
				if e != nil || sha256.Sum256(after) != sha256.Sum256(original) {
					t.Fatal("source archive changed", e)
				}
			}
			if _, err := booktools.GetFormatPath(w.libraryDB, 1, "TXT"); err == nil {
				t.Fatal("editing guard for indexed ZIP was weakened")
			}
		})
	}
}

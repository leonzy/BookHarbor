package httpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/library"
	"testing"
)

func TestMetadataWorkerRepairsCorroboratedLectureIdentity(t *testing.T) {
	w, _, oldPath := referenceFixture(t)
	if err := library.Migrate(w.libraryDB); err != nil {
		t.Fatal(err)
	}
	name := "030919存在与人生-何光沪"
	file := filepath.Join(filepath.Dir(oldPath), name+".txt")
	body := []byte("主讲人简介：\n\n何光沪，1950年生。\n内容简介：\n存在与人生的讲座。\n《存在与人生》（全文）\n正文。")
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.libraryDB.Exec(`UPDATE book_formats SET path=? WHERE book_id=1`, file); err != nil {
		t.Fatal(err)
	}
	if _, err := w.libraryDB.Exec(`UPDATE books SET title=?,sort=? WHERE id=1`, name, name); err != nil {
		t.Fatal(err)
	}
	worker := testMetadataWorker(t, w)
	if err := worker.local(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	b, err := bookGetOne(w.Config(""), 1)
	if err != nil || b.Title != "存在与人生" || len(b.Authors) != 1 || b.Authors[0] != "何光沪" {
		t.Fatal(b, err)
	}
	if jobStatus(t, worker, 1) != "network_pending" {
		t.Fatal("verified author did not reach network queue")
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(body, after) {
		t.Fatal("source changed", err)
	}
}

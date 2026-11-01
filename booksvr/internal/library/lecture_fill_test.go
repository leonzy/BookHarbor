package library

import (
	"encoding/json"
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestLectureFileRepairKeepsManualTitlesAndNetworkBoundaries(t *testing.T) {
	for _, mode := range []string{"file", "network", "locked", "different-title", "different-author"} {
		t.Run(mode, func(t *testing.T) {
			db := newQueryDB(t)
			old := "030919存在与人生-何光沪"
			id, err := ImportBook(db, BookMeta{Title: old, Author: "佚名"})
			if err != nil {
				t.Fatal(err)
			}
			if err := EnsureMetadataSchema(db); err != nil {
				t.Fatal(err)
			}
			if mode == "locked" {
				tx, _ := db.Begin()
				if err := LockMetadata(tx, id, []string{"title"}); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "different-title" {
				old = "人工书名"
				if _, err := db.Exec(`UPDATE books SET title=? WHERE id=?`, old, id); err != nil {
					t.Fatal(err)
				}
			}
			meta := importmeta.Metadata{Title: "存在与人生", Authors: []string{"何光沪"}}
			if mode == "different-author" {
				meta.Authors = []string{"另一人"}
			}
			origin := "file"
			if mode == "network" {
				origin = "network:source"
			}
			if _, err := FillMissingMetadata(db, id, meta, origin); err != nil {
				t.Fatal(err)
			}
			books, err := GetBooksData(db, []int64{id})
			if err != nil {
				t.Fatal(err)
			}
			want := old
			if mode == "file" {
				want = "存在与人生"
			}
			if books[0].Title != want {
				t.Fatal(mode, books[0])
			}
			if mode == "file" {
				var sort, display string
				if err := db.QueryRow(`SELECT b.sort,w.display_title FROM books b JOIN works w ON w.id=b.work_id WHERE b.id=?`, id).Scan(&sort, &display); err != nil || sort != want || display != want {
					t.Fatal(sort, display, err)
				}
				var n int
				oldJSON, _ := json.Marshal(old)
				newJSON, _ := json.Marshal(want)
				if err := db.QueryRow(`SELECT count(*) FROM metadata_enrichment_writes WHERE book_id=? AND field='title' AND origin='file' AND previous_value=? AND written_value=?`, id, string(oldJSON), string(newJSON)).Scan(&n); err != nil || n != 1 {
					t.Fatal(n, err)
				}
			}
		})
	}
}

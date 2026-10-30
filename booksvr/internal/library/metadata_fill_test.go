package library

import (
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestImportPersistsAllExtractedFields(t *testing.T) {
	db := newQueryDB(t)
	id, e := ImportBook(db, BookMeta{Title: "多人作品", Authors: []string{"作者一", "作者二", "作者一"}, ISBN: "9787020091621", Language: "zh", Publisher: "人民文学出版社"})
	if e != nil {
		t.Fatal(e)
	}
	books, e := GetBooksData(db, []int64{id})
	if e != nil {
		t.Fatal(e)
	}
	b := books[0]
	if b.ISBN != "9787020091621" || b.Language != "zh" || len(b.Authors) != 2 || b.Publisher != "人民文学出版社" {
		t.Fatal(b)
	}
}

func TestFillMissingPreservesExistingAndExplicitClears(t *testing.T) {
	db := newQueryDB(t)
	id, e := ImportBook(db, BookMeta{Title: "用户书名", Author: "佚名"})
	if e != nil {
		t.Fatal(e)
	}
	candidate := importmeta.Metadata{Title: "另一书名", Authors: []string{"老舍"}, Publisher: "人民文学出版社", ISBN: "9787020091621", Language: "zh", Comments: "简介"}
	changed, e := FillMissingMetadata(db, id, candidate)
	if e != nil || len(changed) != 5 {
		t.Fatalf("%v %v", changed, e)
	}
	b, e := GetBooksData(db, []int64{id})
	if e != nil {
		t.Fatal(e)
	}
	if b[0].Title != "用户书名" {
		t.Fatal("title overwritten")
	}
	candidate.Authors = []string{"错误作者"}
	candidate.Publisher = "错误出版社"
	candidate.ISBN = "9787020121625"
	changed, e = FillMissingMetadata(db, id, candidate)
	if e != nil || len(changed) > 0 {
		t.Fatalf("existing overwritten: %v %v", changed, e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	tx.Exec(`DELETE FROM identifiers WHERE book=?`, id)
	tx.Exec(`DELETE FROM books_publishers_link WHERE book=?`, id)
	tx.Exec(`DELETE FROM books_authors_link WHERE book=?`, id)
	if e = LockMetadata(tx, id, []string{"isbn", "publisher", "authors"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	changed, e = FillMissingMetadata(db, id, candidate)
	if e != nil || len(changed) > 0 {
		t.Fatalf("manual clear overwritten: %v %v", changed, e)
	}
	b, e = GetBooksData(db, []int64{id})
	if e != nil {
		t.Fatal(e)
	}
	if b[0].ISBN != "" || b[0].Publisher != "" || len(b[0].Authors) > 0 {
		t.Fatal(b)
	}
}

func TestFillMissingRejectsInvalidISBNAndDeletedBook(t *testing.T) {
	db := newQueryDB(t)
	id, e := ImportBook(db, BookMeta{Title: "test"})
	if e != nil {
		t.Fatal(e)
	}
	changed, e := FillMissingMetadata(db, id, importmeta.Metadata{ISBN: "9787020091622"})
	if e != nil || len(changed) != 0 {
		t.Fatal(changed, e)
	}
	if _, e = FillMissingMetadata(db, 99999, importmeta.Metadata{ISBN: "9787020091621"}); e == nil {
		t.Fatal("missing book accepted")
	}
}

func TestHistoricalModifiedBookProtectsEmptyFields(t *testing.T) {
	db := newQueryDB(t)
	id, err := ImportBook(db, BookMeta{Title: "已人工编辑"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE books SET timestamp=1792126389,last_modified='2026-10-20 00:00:00+00:00' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err = ProtectHistoricalMetadataEdits(db, id); err != nil {
		t.Fatal(err)
	}
	changed, err := FillMissingMetadata(db, id, importmeta.Metadata{ISBN: "9787020091621", Publisher: "不能自动填回", Authors: []string{"不能自动填回"}})
	if err != nil || len(changed) > 0 {
		t.Fatal(changed, err)
	}
}

func TestAutomaticMetadataAuditIsAtomicAndRespectsLaterManualEdit(t *testing.T) {
	db := newQueryDB(t)
	id, e := ImportBook(db, BookMeta{Title: "Audit", Author: "佚名"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = FillMissingMetadata(db, id, importmeta.Metadata{Authors: []string{"老舍"}, ISBN: "9787020091621", Publisher: "人民文学出版社"}, "network:fixture")
	if e != nil {
		t.Fatal(e)
	}
	var previous, written, origin string
	if e = db.QueryRow(`SELECT previous_value,written_value,origin FROM metadata_enrichment_writes WHERE book_id=? AND field='isbn'`, id).Scan(&previous, &written, &origin); e != nil {
		t.Fatal(e)
	}
	if previous != `""` || written != `"9787020091621"` || origin != "network:fixture" {
		t.Fatal(previous, written, origin)
	}
	tx, _ := db.Begin()
	LockMetadata(tx, id, []string{"publisher"})
	tx.Exec(`DELETE FROM books_publishers_link WHERE book=?`, id)
	tx.Commit()
	before := 0
	db.QueryRow(`SELECT count(*) FROM metadata_enrichment_writes`).Scan(&before)
	_, e = FillMissingMetadata(db, id, importmeta.Metadata{Publisher: "wrong"}, "network:fixture")
	if e != nil {
		t.Fatal(e)
	}
	after := 0
	db.QueryRow(`SELECT count(*) FROM metadata_enrichment_writes`).Scan(&after)
	if before != after {
		t.Fatal("protected field audited as auto-written")
	}
}

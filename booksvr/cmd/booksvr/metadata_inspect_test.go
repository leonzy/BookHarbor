package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/importmeta"
	"testing"
)

func TestMetadataInspectReadsWithoutDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.txt")
	os.WriteFile(path, []byte("作者：老舍\n出版社：人民文学出版社\nISBN：9787020091621\n"), 0600)
	var out bytes.Buffer
	if err := runMetadataInspect([]string{"-file", path}, &out); err != nil {
		t.Fatal(err)
	}
	var m importmeta.Metadata
	if err := json.Unmarshal(out.Bytes(), &m); err != nil || m.Authors[0] != "老舍" || m.ISBN != "9787020091621" {
		t.Fatal(m, err)
	}
	if err := runMetadataInspect(nil, &out); err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestMetadataOnlyFlag(t *testing.T) {
	opts, err := parseServeOptions([]string{"-metadata-only", "-db", writeTempDB(t), "-addr", "127.0.0.1:8004", "-cookie-secret", "test"})
	if err != nil || !opts.MetadataOnly {
		t.Fatal(opts, err)
	}
}

package httpapi

import (
	"encoding/json"
	"talebook/booksvr/internal/library"
	"testing"
)

func TestSourceStatusDoesNotRetainDiscoveryPathsOrChangePublicResult(t *testing.T) {
	input := library.SourceSyncResult{Checked: 120, Missing: 2, Moved: 3, Roots: map[string]string{"/books": "ok"}, Files: []string{"/books/very-long-title.epub"}, Directories: []string{"/books"}}
	result := sourceStatusResult(input)
	if result.Files != nil || result.Directories != nil {
		t.Fatal("status retains full discovery")
	}
	before, _ := json.Marshal(input)
	after, _ := json.Marshal(result)
	if string(before) != string(after) {
		t.Fatalf("public status changed: %s vs %s", before, after)
	}
	if len(input.Files) != 1 || len(input.Directories) != 1 {
		t.Fatal("caller snapshot was modified")
	}
}

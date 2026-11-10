package scan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"talebook/booksvr/internal/userdb"
	"testing"
)

func TestMinimumSizeCollectionBoundary(t *testing.T) {
	root := t.TempDir()
	for name, size := range map[string]int{"a-readme.txt": 5, "b-small.txt": 10239, "c-exact.txt": 10240, "d-large.txt": 10241} {
		if e := os.WriteFile(filepath.Join(root, name), bytes.Repeat([]byte("x"), size), 0600); e != nil {
			t.Fatal(e)
		}
	}
	_, tasks, skipped, e := CollectTasks(root, 1, nil, map[string]bool{"txt": true}, 10240)
	if e != nil || len(tasks) != 1 || tasks[0].Name != "c-exact.txt" || skipped != 2 {
		t.Fatal(tasks, skipped, e)
	}
}
func TestMinimumSizeAllScanningPaths(t *testing.T) {
	for _, mode := range []string{"legacy", "full", "incremental"} {
		t.Run(mode, func(t *testing.T) {
			s := directoryTestConnections(newTestService(testDB(t)))
			s.MinFileSize = func() int64 { return 10240 }
			root := t.TempDir()
			for name, size := range map[string]int{"a-readme.txt": 6, "b-boundary.txt": 10240, "c-book.txt": 10241} {
				writeBook(t, root, name, bytes.Repeat([]byte("a"), size))
			}
			var e error
			switch mode {
			case "legacy":
				_, e = s.Scan(root, 1)
			case "full":
				_, e = s.ScanAll(context.Background(), root, 1)
			case "incremental":
				e = s.ScanDirectoryQueue(context.Background(), root, false, 1, nil)
			}
			if e != nil {
				t.Fatal(e)
			}
			var small, count int
			s.DB.QueryRow("SELECT count(*) FROM scanfiles WHERE name='a-readme.txt'").Scan(&small)
			s.DB.QueryRow("SELECT count(*) FROM scanfiles WHERE status='ready'").Scan(&count)
			expected := 2
			if mode == "legacy" {
				expected = 1
			}
			if small != 0 || count != expected {
				t.Fatal(small, count, expected)
			}
		})
	}
}
func TestMinimumSizeZIPUsesUncompressedMembersAndRevisitsPolicy(t *testing.T) {
	s := newImportService(t, importTestDB(t), importLibraryDB(t))
	s.SupportedFormats["zip"] = true
	minimum := int64(10240)
	s.MinFileSize = func() int64 { return minimum }
	root := t.TempDir()
	nested := zipTestBytes(t, zipTestEntry{name: "nested-book.txt", body: bytes.Repeat([]byte("n"), 10240)})
	archive := writeZIPFixture(t, root, "compressed.zip", zipTestBytes(t, zipTestEntry{name: "readme.txt", body: []byte("read me")}, zipTestEntry{name: "book.txt", body: bytes.Repeat([]byte("a"), 20480)}, zipTestEntry{name: "nested.zip", body: nested}))
	info, _ := os.Stat(archive)
	if info.Size() >= minimum {
		t.Fatal("fixture must compress below minimum")
	}
	r, e := s.ScanAll(context.Background(), root, 2)
	if e != nil || r.Ready != 2 {
		t.Fatal(r, e)
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM scanfiles WHERE name='readme.txt'").Scan(&n)
	if n != 0 {
		t.Fatal("small ZIP member was registered")
	}
	minimum = 0
	r, e = s.ScanAll(context.Background(), root, 2)
	if e != nil || r.Ready != 1 {
		t.Fatal("lowered policy did not revisit ZIP", r, e)
	}
}
func TestMinimumSizeIncrementalRevisitsUnchangedDirectory(t *testing.T) {
	s := directoryTestConnections(newTestService(testDB(t)))
	minimum := int64(10240)
	s.MinFileSize = func() int64 { return minimum }
	root := t.TempDir()
	writeBook(t, root, "small.txt", bytes.Repeat([]byte("b"), 500))
	if e := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); e != nil {
		t.Fatal(e)
	}
	minimum = 0
	if e := s.ScanDirectoryQueue(context.Background(), root, false, 1, nil); e != nil {
		t.Fatal(e)
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM scanfiles WHERE status='ready'").Scan(&n)
	if n != 1 {
		t.Fatal("policy change hidden by directory stamp", n)
	}
}
func TestMinimumSizeBlocksOldPendingImportAndCanRescan(t *testing.T) {
	s := newImportService(t, importTestDB(t), importLibraryDB(t))
	root := t.TempDir()
	file := makeValidEpub(t, root, "small.epub")
	row := seedReadyRow(t, s.DB, file, "sha256:old-ready")
	minimum := int64(10240)
	s.MinFileSize = func() int64 { return minimum }
	r, e := s.Import(nil, 1, false, ModeMove)
	if e != nil || r.Dropped != 1 || r.Imported != 0 {
		t.Fatal(r, e)
	}
	saved, e := getScanFileByPath(s.DB, file)
	if e != nil || saved.Status != userdb.ScanStatusDrop || saved.Data["skip_reason_code"] != "file.too_small" {
		t.Fatal(saved, e)
	}
	if _, e = os.Stat(file); e != nil {
		t.Fatal("filtered file must not be moved or removed", e)
	}
	var n int
	s.LibraryDB.QueryRow("SELECT count(*) FROM books").Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	minimum = 0
	result, e := s.Scan(root, 10)
	if e != nil || result.Ready != 1 {
		t.Fatal("old filtered pending row not retryable", row.ID, result, e)
	}
}

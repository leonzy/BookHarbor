package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talebook/booksvr/internal/library"
)

type sourceBlockedOutput struct{}

func (sourceBlockedOutput) Write(p []byte) (int, error) {
	n, e := os.Stdout.Write(p)
	if bytes.Contains(p, []byte(`"type":"directory"`)) {
		time.Sleep(time.Hour)
	}
	return n, e
}

func TestSourceProcessHelper(t *testing.T) {
	mode := os.Getenv("BOOKHARBOR_TEST_SOURCE_PROCESS")
	if mode == "" {
		return
	}
	if mode == "empty" {
		os.Exit(0)
	}
	if mode == "bad" {
		fmt.Fprintln(os.Stdout, `{"type":"unknown"}`)
		os.Exit(0)
	}
	if mode == "error" {
		fmt.Fprint(os.Stderr, strings.Repeat("X", 50000))
		os.Exit(1)
	}
	var out io.Writer = os.Stdout
	if mode == "block" {
		out = sourceBlockedOutput{}
	}
	if err := RunSourceSyncWorker(context.Background(), os.Stdin, out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSourceProcessSameResultAndLiveDirectoryEvents(t *testing.T) {
	db, path := newTestDBFile(t)
	if e := library.Migrate(db); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "books,with,commas")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "book.txt")
	if e := os.WriteFile(file, []byte("book"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,'TXT',?,4,0)`, file); e != nil {
		t.Fatal(e)
	}
	var schemaBefore int
	db.QueryRow(`PRAGMA schema_version`).Scan(&schemaBefore)
	t.Setenv("BOOKHARBOR_TEST_SOURCE_PROCESS", "worker")
	request := sourceProcessRequest{Version: 1, DBPath: path, Roots: []string{root}}
	command := []string{os.Args[0], "-test.run=^TestSourceProcessHelper$"}
	visited := 0
	late := filepath.Join(root, "created-during-watch-install")
	result, e := runFullSourceProcess(context.Background(), command, request, func(p string) error {
		visited++
		if visited == 1 {
			time.Sleep(30 * time.Millisecond)
			if e := os.Mkdir(late, 0700); e != nil {
				return e
			}
		}
		var states int
		if err := db.QueryRow(`SELECT count(*) FROM source_sync_formats`).Scan(&states); err != nil {
			return err
		}
		if states != 0 {
			return fmt.Errorf("directory event arrived after committing source state")
		}
		return nil
	})
	if e != nil || result.Checked != 1 || visited == 0 || len(result.Directories) == 0 || len(result.Files) != 0 {
		t.Fatal(result, visited, e)
	}
	found := false
	for _, p := range result.Directories {
		if p == late {
			found = true
		}
	}
	if !found {
		t.Fatal("child scanned before watcher acknowledgement", result.Directories)
	}
	var schemaAfter int
	db.QueryRow(`PRAGMA schema_version`).Scan(&schemaAfter)
	if schemaBefore != schemaAfter {
		t.Fatal("child migrated database")
	}
	moved := filepath.Join(root, "renamed.txt")
	if e = os.Rename(file, moved); e != nil {
		t.Fatal(e)
	}
	result, e = runFullSourceProcess(context.Background(), command, request, nil)
	if e != nil || result.Moved != 1 || result.Missing != 0 {
		t.Fatal(result, e)
	}
	var actual string
	db.QueryRow(`SELECT path FROM book_formats WHERE book_id=1 AND format='TXT'`).Scan(&actual)
	if actual != moved {
		t.Fatal(actual)
	}
	if e = os.Remove(moved); e != nil {
		t.Fatal(e)
	}
	// Block inside DirectoryVisited before availability updates, then cancel the
	// actual worker. SQLite state must retain its last committed availability.
	t.Setenv("BOOKHARBOR_TEST_SOURCE_PROCESS", "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, e = runFullSourceProcess(ctx, command, request, func(string) error { cancel(); return nil })
	if e != context.Canceled {
		t.Fatal(e)
	}
	var missing int
	if e = db.QueryRow(`SELECT missing FROM source_sync_formats WHERE format_id=(SELECT id FROM book_formats WHERE book_id=1 AND format='TXT')`).Scan(&missing); e != nil || missing != 0 {
		t.Fatal(missing, e)
	}
}

func TestSourceProcessProtocolFailuresAreBounded(t *testing.T) {
	for _, mode := range []string{"empty", "bad", "error"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BOOKHARBOR_TEST_SOURCE_PROCESS", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, e := runFullSourceProcess(ctx, []string{os.Args[0], "-test.run=^TestSourceProcessHelper$"}, sourceProcessRequest{Version: 1}, nil)
			if e == nil || len(e.Error()) > 9000 {
				t.Fatal(e)
			}
		})
	}
}

func TestSourceProcessDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	request, _ := json.Marshal(sourceProcessRequest{Version: 1, DBPath: path, Roots: []string{t.TempDir()}})
	if e := RunSourceSyncWorker(context.Background(), bytes.NewReader(request), io.Discard); e == nil {
		t.Fatal("missing DB accepted")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("worker created database", e)
	}
	if e := RunSourceSyncWorker(context.Background(), strings.NewReader(`{"version":2,"db":"ignored","roots":["ignored"]}`), io.Discard); e == nil {
		t.Fatal("unknown protocol accepted")
	}
}

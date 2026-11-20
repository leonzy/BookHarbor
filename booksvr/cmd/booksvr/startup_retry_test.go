package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupRetriesRealSQLiteLock(t *testing.T) {
	file := filepath.Join(t.TempDir(), "busy.db")
	writer, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.Exec("CREATE TABLE t(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO t VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = retryStartup(ctx, io.Discard, func() error {
		attempts++
		_, e := db.Exec("INSERT INTO t VALUES(2)")
		if attempts == 1 {
			if e == nil {
				t.Fatal("second writer acquired the lock")
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			return fmt.Errorf("migration: %w", e)
		}
		return e
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	permanent := errors.New("database is locked (untyped error)")
	if err := retryStartup(ctx, io.Discard, func() error { return permanent }); err != permanent {
		t.Fatal(err)
	}
	attempts = 0
	err = retryStartup(ctx, io.Discard, func() error {
		attempts++
		_, e := db.Exec("SELECT * FROM absent_table")
		return e
	})
	if err == nil || attempts != 1 {
		t.Fatalf("permanent SQLite failure: attempts=%d err=%v", attempts, err)
	}
	tx, err = writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO t VALUES(3)"); err != nil {
		t.Fatal(err)
	}
	err = retryStartup(ctx, io.Discard, func() error {
		_, lockErr := db.Exec("INSERT INTO t VALUES(4)")
		cancel()
		return lockErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

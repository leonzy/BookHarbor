package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"talebook/booksvr/internal/library"
)

func titleIndexHasSpace(dbPath string) bool {
	free, err := statfsFree(filepath.Dir(dbPath))
	if err != nil || free < 4<<30 {
		return false
	}
	wal, err := os.Stat(dbPath + "-wal")
	return os.IsNotExist(err) || err == nil && wal.Size() < 512<<20
}

func attachTitleIndexWorker(parent context.Context, db *sql.DB, dbPath string, out io.Writer) func() {
	if dbPath == "" || db == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		var wasBlocked bool
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if !titleIndexHasSpace(dbPath) {
				if !wasBlocked && out != nil {
					fmt.Fprintln(out, "title index: paused due to low disk space or a large SQLite WAL")
				}
				wasBlocked = true
				timer.Reset(time.Minute)
				continue
			}
			wasBlocked = false
			workCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			worked, err := library.IndexTitleBatch(workCtx, db)
			stop()
			delay := 5 * time.Second
			if err != nil {
				delay = 30 * time.Second
				if ctx.Err() == nil && out != nil {
					fmt.Fprintf(out, "title index: %v\n", err)
				}
			} else if worked {
				delay = 250 * time.Millisecond
			}
			timer.Reset(delay)
		}
	}()
	return func() { cancel(); <-done }
}

package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"talebook/booksvr/internal/library"
	"time"
)

// Only the main service runs reconciliation. SQLite triggers also capture
// changes made by an already-running, older metadata enrichment process.
func attachIdentityWorker(parent context.Context, db *sql.DB, out io.Writer) func() {
	if !library.IdentityAvailable(db) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		owner := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixNano())
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if _, err := library.RunIdentityBatch(ctx, db, owner, 10); err != nil && ctx.Err() == nil && out != nil {
				fmt.Fprintf(out, "book identity: %v\n", err)
			}
		}
	}()
	return func() { cancel(); <-done }
}

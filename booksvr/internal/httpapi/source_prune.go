package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/userdb"
)

// A separate bounded connection never holds an import transaction or waits on
// its default 30s busy timeout. Outbox replay touches completed imports only.
func startSourcePruneWorker(ctx context.Context, opts ServeOptions, books, users *sql.DB, ready func() bool) func() {
	var owned *sql.DB
	if opts.DBPath != "" {
		var e error
		owned, e = openExistingDB(opts.DBPath, 100)
		if e != nil {
			if opts.Out != nil {
				fmt.Fprintf(opts.Out, "source cleanup: %v\n", e)
			}
			return func() {}
		}
		owned.SetMaxOpenConns(1)
		owned.SetMaxIdleConns(1)
		books = owned
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var cursor int64
		next := time.Time{}
		lastReport := time.Time{}
		report := func(e error) {
			if ctx.Err() == nil && opts.Out != nil && time.Since(lastReport) >= 30*time.Second {
				fmt.Fprintf(opts.Out, "source cleanup: %v\n", e)
				lastReport = time.Now()
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if time.Now().Before(next) || !ready() {
				continue
			}
			if e := flushSourcePruneRemovals(books, users); e != nil {
				report(e)
				next = time.Now().Add(time.Second)
				continue
			}
			candidates, e := library.MissingSourceCandidates(ctx, books, cursor, time.Now(), 5)
			if e != nil {
				report(e)
				next = time.Now().Add(time.Second)
				continue
			}
			if len(candidates) == 0 {
				cursor = 0
				next = time.Now().Add(30 * time.Second)
				continue
			}
			roots := importdir.ConfiguredAllowedRoots(LiveImportDir(opts.ImportDir, opts.SettingsPath))
			_, e = library.PruneMissingSources(ctx, books, roots, opts.LibraryRoot, candidates, time.Now(), ready)
			if e != nil {
				report(e)
				next = time.Now().Add(time.Second)
				continue
			}
			cursor = candidates[len(candidates)-1].ID
		}
	}()
	return func() {
		<-done
		if owned != nil {
			owned.Close()
		}
	}
}

func flushSourcePruneRemovals(books, users *sql.DB) error {
	pending, e := library.SourcePruneRemovals(books)
	if e != nil {
		return e
	}
	ids := make([]int64, 0, len(pending))
	sources := make([]userdb.MissingIndexedSource, 0, len(pending))
	for _, r := range pending {
		ids = append(ids, r.ID)
		sources = append(sources, userdb.MissingIndexedSource{BookID: r.BookID, Path: r.Path, RemovedBook: r.RemovedBook})
	}
	if e = userdb.DropMissingIndexedSources(users, sources); e != nil {
		return e
	}
	return library.AckSourcePrunes(books, ids)
}

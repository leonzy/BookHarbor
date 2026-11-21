package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"talebook/booksvr/internal/importdir"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcewatch"
	"talebook/booksvr/internal/userdb"
)

// Source reconciliation remains enabled when automatic NEW imports are off.
func attachSourceWorker(parent context.Context, opts ServeOptions, books, users *sql.DB) (ServeOptions, func()) {
	if books == nil || users == nil {
		return opts, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	status := map[string]any{"state": "starting"}
	previous := opts.ImportWatchStatus
	opts.ImportWatchStatus = func() map[string]any {
		out := map[string]any{}
		if previous != nil {
			out = previous()
		}
		mu.Lock()
		defer mu.Unlock()
		copy := map[string]any{}
		for k, v := range status {
			copy[k] = v
		}
		out["source_sync"] = copy
		return out
	}
	done := make(chan struct{})
	stopPrunes := startSourcePruneWorker(ctx, opts, books, users, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return status["state"] == "watching"
	})
	go func() {
		defer close(done)
		watcher, watchErr := sourcewatch.New(ctx)
		var events <-chan struct{}
		if watcher != nil {
			defer watcher.Close()
			events = watcher.Events()
		}
		seeded := false
		initialized := false
		directories := map[string]bool{}
		var lastRoots []string
		var lastHealth map[string]string
		var lastFull time.Time
		var pending sourceSyncPending
		run := func() {
			started := time.Now()
			dir := LiveImportDir(opts.ImportDir, opts.SettingsPath)
			roots := importdir.ConfiguredAllowedRoots(dir)
			health, err := library.CheckSourceRoots(ctx, books, roots)
			overflow := false
			if watcher != nil {
				for _, path := range watcher.Changes() {
					if pending.paths == nil {
						pending.paths = make(map[string]struct{})
					}
					pending.paths[path] = struct{}{}
				}
				overflow = watcher.Overflowed()
				for _, m := range watcher.Moves() {
					pending.moves = append(pending.moves, library.SourceMove{From: m.From, To: m.To})
				}
			}
			wasInitialized := initialized
			pending.overflow = pending.overflow || overflow
			pending.baseline = pending.baseline || sourceNeedsBaseline(initialized, !reflect.DeepEqual(roots, lastRoots), pending.overflow, len(pending.paths) > 0, lastHealth, health)
			// Failed event monitoring is visible and falls back at a bounded
			// interval, rather than repeatedly loading the whole library.
			if (watcher == nil || watchErr != nil) && time.Since(lastFull) >= 15*time.Minute {
				pending.baseline = true
			}
			if len(pending.paths)+len(pending.moves) > 32768 {
				// An exhausted event buffer is equivalent to watcher overflow.
				pending.baseline = true
				pending.overflow = true
				pending.paths, pending.moves = nil, nil
			}
			if err == nil {
				lastHealth = health
			}
			if started.Before(pending.retryAt) {
				return
			}
			full := pending.baseline
			changed := make([]string, 0, len(pending.paths))
			for path := range pending.paths {
				changed = append(changed, path)
			}
			moves := pending.moves
			if err == nil && full {
				moves = nil
				// A watch can still refer to the underlying directory after a
				// mount is restored. Reinstall during baseline discovery.
				if watcher != nil {
					watchErr = watcher.SetDirectories(nil)
				}
			}
			var result library.SourceSyncResult
			result.Roots = health
			result.CheckedAt = time.Now().Format(time.RFC3339)
			if err == nil && (full || len(changed) > 0) {
				mu.Lock()
				status["state"] = "scanning"
				mu.Unlock()
				if !seeded {
					err = userdb.EnsureSourceSyncIndexes(users)
					var needed bool
					if err == nil {
						needed, err = library.NeedsSourceHashes(books, opts.LibraryRoot)
					}
					if err == nil && needed {
						var hashes map[string]string
						hashes, err = userdb.IndexedSourceHashes(users)
						if err == nil {
							err = library.SeedSourceHashes(books, hashes)
						}
					}
					seeded = err == nil
				}
				option := library.SourceSyncOptions{ChangedPaths: changed, FullDiscovery: true, Incremental: !full, Moves: moves, OmitFilePaths: true}
				if watcher != nil {
					option.DirectoryVisited = func(path string) error {
						directories[path] = true // retain parents even if a deeper subtree is unreadable
						e := watcher.AddDirectory(path)
						if e != nil {
							watchErr = e
						}
						return nil
					}
				}
				if err == nil {
					if full && opts.FullSourceSyncExecutable != "" {
						result, err = runFullSourceProcess(ctx, []string{opts.FullSourceSyncExecutable, "source-sync-worker"}, sourceProcessRequest{Version: 1, DBPath: opts.DBPath, LibraryRoot: opts.LibraryRoot, Roots: roots}, option.DirectoryVisited)
					} else {
						result, err = library.SyncSourceFiles(ctx, books, roots, opts.LibraryRoot, option)
					}
				}
				if err == nil {
					// Preserve watches for protected roots. Only a healthy
					// changed subtree replaces its previous directory listing.
					for path := range directories {
						configured := false
						for _, root := range roots {
							if importdir.IsWithin(path, root) {
								configured = true
								break
							}
						}
						if !configured {
							delete(directories, path)
							continue
						}
						for root, h := range result.Roots {
							if h != "ok" || !importdir.IsWithin(path, root) {
								continue
							}
							remove := full
							for _, p := range changed {
								if importdir.IsWithin(path, p) {
									remove = true
								}
							}
							if remove {
								delete(directories, path)
							}
						}
					}
					for _, p := range result.Directories {
						directories[p] = true
					}
					lastRoots = append([]string(nil), roots...)
					initialized = true
					if full {
						lastFull = time.Now()
					}
					// Only successful baseline creation changes the cheap health
					// transition from uninitialized to ok. A deep scan error
					// must not turn each 30-second health tick into a full scan.
					for root, h := range result.Roots {
						if h == "ok" {
							health[root] = "ok"
						}
					}
					if opts.QueueSourcePaths != nil {
						opts.QueueSourcePaths(sourceImportHints(changed, roots), pending.overflow || full && wasInitialized)
					}
					pending = sourceSyncPending{}
				}
				if err != nil {
					pending.deferRetry(time.Now())
				}
			}
			if err == nil {
				err = flushSourceChanges(books, users)
			}
			if watcher != nil {
				var dirs []string
				for p := range directories {
					dirs = append(dirs, p)
				}
				dirs = append(dirs, roots...)
				watchErr = watcher.SetDirectories(dirs)
			}
			mu.Lock()
			// These private fields are not serialized. Retaining them here pins
			// an entire discovery snapshot until the next worker tick.
			status["result"] = sourceStatusResult(result)
			status["duration_ms"] = time.Since(started).Milliseconds()
			status["state"] = "watching"
			status["mode"] = "events+incremental"
			if watchErr != nil {
				status["mode"] = "reconcile"
				status["watch_error"] = watchErr.Error()
			} else {
				delete(status, "watch_error")
			}
			if err != nil {
				status["state"] = "error"
				status["error"] = err.Error()
			} else {
				delete(status, "error")
			}
			mu.Unlock()
			if err != nil && ctx.Err() == nil && opts.Out != nil {
				fmt.Fprintf(opts.Out, "source sync: %v\n", err)
			}
		}
		run()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var debounce *time.Timer
		var eventDue <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if debounce != nil {
					debounce.Stop()
				}
				return
			case <-events:
				if debounce == nil {
					debounce = time.NewTimer(time.Second)
					eventDue = debounce.C
				}
			case <-eventDue:
				debounce = nil
				eventDue = nil
				run()
			case <-ticker.C:
				run()
			}
		}
	}()
	return opts, func() { cancel(); <-done; stopPrunes() }
}

func sourceStatusResult(result library.SourceSyncResult) library.SourceSyncResult {
	result.Files = nil
	result.Directories = nil
	return result
}

type sourceSyncPending struct {
	paths    map[string]struct{}
	moves    []library.SourceMove
	baseline bool
	overflow bool
	retryAt  time.Time
}

func (p *sourceSyncPending) deferRetry(now time.Time) {
	delay := 30 * time.Second
	if p.baseline {
		delay = 15 * time.Minute
	}
	p.retryAt = now.Add(delay)
}

// New-file discovery can accept events from a protected root without making
// a deletion decision. Ignore health marker events and paths outside the roots.
func sourceImportHints(changed, roots []string) []string {
	var paths []string
	for _, path := range changed {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		for _, root := range roots {
			if importdir.IsWithin(path, root) {
				paths = append(paths, path)
				break
			}
		}
	}
	return paths
}

func flushSourceChanges(books, users *sql.DB) error {
	for {
		changes, err := library.SourceChanges(books)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		for _, change := range changes {
			if err = userdb.RelocateIndexedSource(users, change.BookID, change.OldPath, change.NewPath); err != nil {
				return err
			}
			if err = library.AckSourceChange(books, change.ID); err != nil {
				return err
			}
		}
	}
}

func sourceNeedsBaseline(initialized, rootsChanged, overflow, dirty bool, previous, current map[string]string) bool {
	if !initialized || rootsChanged || overflow {
		return true
	}
	for root, h := range current {
		if h == "ok" && previous[root] != h {
			return true
		}
		if h == "uninitialized" && (dirty || previous[root] != h) {
			return true
		}
	}
	return false
}

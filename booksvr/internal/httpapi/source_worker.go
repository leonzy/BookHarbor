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
		var retry bool
		run := func() {
			started := time.Now()
			dir := LiveImportDir(opts.ImportDir, opts.SettingsPath)
			roots := importdir.ConfiguredAllowedRoots(dir)
			health, err := library.CheckSourceRoots(ctx, books, roots)
			var changed []string
			var moves []library.SourceMove
			overflow := false
			if watcher != nil {
				changed = watcher.Changes()
				overflow = watcher.Overflowed()
				for _, m := range watcher.Moves() {
					moves = append(moves, library.SourceMove{From: m.From, To: m.To})
				}
			}
			wasInitialized := initialized
			full := retry || sourceNeedsBaseline(initialized, !reflect.DeepEqual(roots, lastRoots), overflow, len(changed) > 0, lastHealth, health)
			// Failed event monitoring is visible and falls back at a bounded
			// interval, rather than repeatedly loading the whole library.
			if (watcher == nil || watchErr != nil) && time.Since(lastFull) >= 15*time.Minute {
				full = true
			}
			if full {
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
				option := library.SourceSyncOptions{ChangedPaths: changed, FullDiscovery: true, Incremental: !full, Moves: moves}
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
					result, err = library.SyncSourceFiles(ctx, books, roots, opts.LibraryRoot, option)
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
				}

			}
			if err == nil {
				err = flushSourceChanges(books, users)
			}
			// Queue events even when deletion synchronization is protected:
			// discovery itself never deletes anything. Reject out-of-root
			// and health-marker noise before the import worker sees it.
			if err == nil && opts.QueueSourcePaths != nil {
				var paths []string
				for _, p := range changed {
					if strings.HasPrefix(filepath.Base(p), ".") {
						continue
					}
					for _, root := range roots {
						if importdir.IsWithin(p, root) {
							paths = append(paths, p)
							break
						}
					}
				}
				opts.QueueSourcePaths(paths, overflow || full && wasInitialized)
			}
			if watcher != nil {
				var dirs []string
				for p := range directories {
					dirs = append(dirs, p)
				}
				dirs = append(dirs, roots...)
				watchErr = watcher.SetDirectories(dirs)
			}
			retry = err != nil
			lastHealth = health
			mu.Lock()
			status["result"] = result
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
		var pending <-chan time.Time
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
					pending = debounce.C
				}
			case <-pending:
				debounce = nil
				pending = nil
				run()
			case <-ticker.C:
				run()
			}
		}
	}()
	return opts, func() { cancel(); <-done }
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

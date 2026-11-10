package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"talebook/booksvr/internal/library"
)

// RunSourcePruneWorker is an explicit, bounded maintenance operation, not HTTP.
// It reuses exactly the background worker's guards and never migrates databases.
func RunSourcePruneWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	var req struct {
		DB          string   `json:"db"`
		Users       string   `json:"users"`
		Roots       []string `json:"roots"`
		LibraryRoot string   `json:"library_root"`
		After       int64    `json:"after"`
		Limit       int      `json:"limit"`
		Apply       bool     `json:"apply"`
	}
	d := json.NewDecoder(io.LimitReader(in, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		return e
	}
	if req.DB == "" || req.Users == "" || len(req.Roots) == 0 || req.Limit <= 0 || req.Limit > 500 {
		return fmt.Errorf("invalid source prune request")
	}
	books, e := openExistingDB(req.DB, 100)
	if e != nil {
		return e
	}
	defer books.Close()
	books.SetMaxOpenConns(1)
	users, e := openExistingDB(req.Users, 100)
	if e != nil {
		return e
	}
	defer users.Close()
	users.SetMaxOpenConns(1)
	candidates, e := library.MissingSourceCandidates(ctx, books, req.After, time.Now(), req.Limit)
	if e != nil {
		return e
	}
	removed := 0
	cursor := req.After
	if req.Apply {
		if e = flushSourcePruneRemovals(books, users); e != nil {
			return e
		}
	}
	for start := 0; start < len(candidates); start += 5 {
		end := min(start+5, len(candidates))
		if req.Apply {
			n, e := library.PruneMissingSources(ctx, books, req.Roots, req.LibraryRoot, candidates[start:end], time.Now())
			if e != nil {
				return e
			}
			removed += n
		}
		cursor = candidates[end-1].ID
	}
	if req.Apply {
		if e = flushSourcePruneRemovals(books, users); e != nil {
			return e
		}
	}
	return json.NewEncoder(out).Encode(map[string]any{"candidates": len(candidates), "removed": removed, "after": cursor, "applied": req.Apply})
}

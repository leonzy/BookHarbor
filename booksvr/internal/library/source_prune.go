package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"talebook/booksvr/internal/sourcefile"
)

type MissingSourceCandidate struct {
	ID   int64
	Path string
}

type sourcePruneBatchKey struct{}
type sourcePruneBatch struct {
	tx     *sql.Tx
	guards []func() bool
}

var errSourcePruneProtected = errors.New("source became protected before cleanup commit")

// A maximum of five versions share a commit. Recheck every source after the
// entire batch so a late mount failure rolls back all earlier removals too.
func PruneMissingSources(ctx context.Context, db *sql.DB, roots []string, libraryRoot string, candidates []MissingSourceCandidate, now time.Time, allow ...func() bool) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	if len(candidates) > 5 {
		return 0, fmt.Errorf("source cleanup batch exceeds five versions")
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE source_prune_pending SET seen_at=seen_at WHERE format_id=?`, candidates[0].ID); e != nil {
		return 0, e
	}
	batch := &sourcePruneBatch{tx: tx}
	ctx = context.WithValue(ctx, sourcePruneBatchKey{}, batch)
	removed := 0
	for _, c := range candidates {
		ok, e := PruneMissingSource(ctx, db, roots, libraryRoot, c, now, allow...)
		if e != nil {
			if errors.Is(e, errSourcePruneProtected) {
				return 0, nil
			}
			return 0, e
		}
		if ok {
			removed++
		}
	}
	for _, guard := range batch.guards {
		if !guard() {
			return 0, nil
		}
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return removed, nil
}

type RemovedSource struct {
	ID, BookID  int64
	Path        string
	RemovedBook bool
}

func SourcePruneRemovals(db *sql.DB) ([]RemovedSource, error) {
	rows, e := db.Query(`SELECT id,book_id,path,removed_book FROM source_prune_removed WHERE applied=0 ORDER BY id LIMIT 25`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []RemovedSource
	for rows.Next() {
		var r RemovedSource
		if e = rows.Scan(&r.ID, &r.BookID, &r.Path, &r.RemovedBook); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func AckSourcePrune(db *sql.DB, id int64) error {
	return AckSourcePrunes(db, []int64{id})
}

func AckSourcePrunes(db *sql.DB, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, e := db.Exec(`UPDATE source_prune_removed SET applied=1 WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	return e
}

// MissingSourceCandidates walks only the indexed confirmed-missing queue.
func MissingSourceCandidates(ctx context.Context, db *sql.DB, after int64, now time.Time, limit int) ([]MissingSourceCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT format_id,path FROM source_prune_pending WHERE format_id>? AND seen_at<=? ORDER BY format_id LIMIT ?`, after, now.Add(-time.Minute).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissingSourceCandidate
	for rows.Next() {
		var c MissingSourceCandidate
		if err = rows.Scan(&c.ID, &c.Path); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneMissingSource never unlinks bytes. Missing states are produced after
// relocation discovery; a delay also permits split move events to arrive.
func PruneMissingSource(ctx context.Context, db *sql.DB, roots []string, libraryRoot string, candidate MissingSourceCandidate, now time.Time, allow ...func() bool) (bool, error) {
	batch, _ := ctx.Value(sourcePruneBatchKey{}).(*sourcePruneBatch)
	var query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	} = db
	if batch != nil {
		query = batch.tx
	}
	physical := candidate.Path
	if sourcefile.IsReference(physical) {
		ref, e := sourcefile.Decode(physical)
		if e != nil {
			return false, nil
		}
		physical = ref.Archive
	}
	if !filepath.IsAbs(physical) || libraryRoot != "" && sourceWithin(physical, libraryRoot) || sourcePathHasSymlink(physical) {
		return false, nil
	}
	type guard struct{ root, identity, token string }
	var guards []guard
	for _, root := range normalizeSourceRoots(roots) {
		if !sourceWithin(physical, root) {
			continue
		}
		g := guard{root: root}
		if e := query.QueryRowContext(ctx, `SELECT identity,token FROM source_sync_roots WHERE root=?`, root).Scan(&g.identity, &g.token); e != nil {
			if e == sql.ErrNoRows {
				return false, nil
			}
			return false, e
		}
		if strings.HasPrefix(g.identity, "unverified:") {
			return false, nil
		}
		guards = append(guards, g)
	}
	if len(guards) == 0 {
		return false, nil
	}
	healthy := func() bool {
		for _, check := range allow {
			if check != nil && !check() {
				return false
			}
		}
		for _, g := range guards {
			info, e := os.Stat(g.root)
			if e != nil || !info.IsDir() || g.identity == "" || sourceRootIdentity(g.root, info) != g.identity {
				return false
			}
			if g.token != "" {
				b, e := os.ReadFile(filepath.Join(g.root, ".talebook-source-health"))
				if e != nil || string(b) != g.token {
					return false
				}
			}
		}
		_, e := os.Lstat(physical)
		return os.IsNotExist(e) && !sourcePathHasSymlink(physical)
	}
	if !healthy() {
		return false, nil
	}
	var tx *sql.Tx
	var e error
	if batch != nil {
		tx = batch.tx
	} else {
		tx, e = db.BeginTx(ctx, nil)
		if e != nil {
			return false, e
		}
		defer tx.Rollback()
	}
	// Acquire the write reservation before any snapshot reads.
	if _, e = tx.ExecContext(ctx, `UPDATE source_prune_pending SET seen_at=seen_at WHERE format_id=? AND path=?`, candidate.ID, candidate.Path); e != nil {
		return false, e
	}
	var book int64
	var format string
	e = tx.QueryRowContext(ctx, `SELECT f.book_id,f.format FROM source_prune_pending p JOIN book_formats f ON f.id=p.format_id AND f.path=p.path JOIN source_sync_formats s ON s.format_id=f.id WHERE p.format_id=? AND p.path=? AND p.seen_at<=? AND s.missing=1`, candidate.ID, candidate.Path, now.Add(-time.Minute).Unix()).Scan(&book, &format)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	for _, g := range guards {
		var identity, token string
		if e = tx.QueryRowContext(ctx, `SELECT identity,token FROM source_sync_roots WHERE root=?`, g.root).Scan(&identity, &token); e != nil {
			return false, e
		}
		if identity != g.identity || token != g.token {
			return false, nil
		}
	}
	if !healthy() {
		return false, nil
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM book_formats WHERE id=? AND path=?`, candidate.ID, candidate.Path); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM data WHERE book=? AND UPPER(format)=UPPER(?) AND NOT EXISTS(SELECT 1 FROM book_formats WHERE book_id=? AND UPPER(format)=UPPER(?))`, book, format, book, format); e != nil {
		return false, e
	}
	var remaining int
	if e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM book_formats WHERE book_id=?)+(SELECT count(*) FROM data WHERE book=?)`, book, book).Scan(&remaining); e != nil {
		return false, e
	}
	removedBook := 0
	if remaining == 0 {
		if e = deleteBookTx(tx, book); e != nil {
			return false, e
		}
		removedBook = 1
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO source_prune_removed(format_id,book_id,path,format,removed_book) VALUES(?,?,?,?,?)`, candidate.ID, book, candidate.Path, format, removedBook); e != nil {
		return false, e
	}
	if !healthy() {
		if batch != nil {
			return false, errSourcePruneProtected
		}
		return false, nil
	}
	if batch != nil {
		batch.guards = append(batch.guards, healthy)
		return true, nil
	}
	return true, tx.Commit()
}

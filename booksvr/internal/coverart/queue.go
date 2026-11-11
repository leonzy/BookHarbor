package coverart

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"talebook/booksvr/internal/sourcefile"
	"time"
)

func MigrateQueue(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS import_cover_jobs(book_id INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,source TEXT NOT NULL,format TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,retry_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '')`)
	if err != nil {
		return err
	}
	rows, err := db.Query(`PRAGMA table_info(import_cover_jobs)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var initial any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &initial, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, col := range []struct{ name, ddl string }{{"state", "TEXT NOT NULL DEFAULT 'pending'"}, {"fingerprint", "TEXT NOT NULL DEFAULT ''"}, {"checked_at", "INTEGER NOT NULL DEFAULT 0"}, {"version", "INTEGER NOT NULL DEFAULT 0"}} {
		if !columns[col.name] {
			if _, err = db.Exec(`ALTER TABLE import_cover_jobs ADD COLUMN ` + col.name + ` ` + col.ddl); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS import_cover_ready ON import_cover_jobs(retry_at,book_id) WHERE state<>'failed'`,
		`CREATE INDEX IF NOT EXISTS import_cover_recheck ON import_cover_jobs(checked_at,book_id) WHERE state IN ('failed','blocked')`,
		`CREATE TRIGGER IF NOT EXISTS import_cover_book_delete AFTER DELETE ON books BEGIN DELETE FROM import_cover_jobs WHERE book_id=OLD.id; END`,
	} {
		if _, err = db.Exec(q); err != nil {
			return err
		}
	}
	var hasFormats int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='book_formats'`).Scan(&hasFormats); err != nil {
		return err
	}
	if hasFormats > 0 {
		if _, err = db.Exec(`CREATE TRIGGER IF NOT EXISTS import_cover_relocated AFTER UPDATE OF path ON book_formats WHEN OLD.path<>NEW.path BEGIN UPDATE import_cover_jobs SET source=NEW.path,state='pending',attempts=0,retry_at=0,error='',fingerprint='',checked_at=0,version=version+1 WHERE book_id=NEW.book_id AND UPPER(format)=UPPER(NEW.format); END`); err != nil {
			return err
		}
	}
	// Classify legacy failures without rerunning their expensive extraction.
	rows, err = db.Query(`SELECT book_id,source,attempts,error FROM import_cover_jobs WHERE state='pending' AND error<>''`)
	if err != nil {
		return err
	}
	type oldJob struct {
		id              int64
		source, message string
		attempts        int
	}
	var old []oldJob
	for rows.Next() {
		var j oldJob
		if err = rows.Scan(&j.id, &j.source, &j.attempts, &j.message); err != nil {
			rows.Close()
			return err
		}
		old = append(old, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range old {
		state := failureState(fmt.Errorf("%s", j.message), j.attempts)
		fp := sourceFingerprint(j.source)
		if fp == "unavailable" {
			state = "blocked"
		}
		if _, err = db.Exec(`UPDATE import_cover_jobs SET state=?,fingerprint=?,checked_at=? WHERE book_id=? AND state='pending' AND error=?`, state, fp, time.Now().Unix(), j.id, j.message); err != nil {
			return err
		}
	}
	return nil
}

func Queue(db *sql.DB, id int64, source, format string) error {
	if !Supported(format) {
		return nil
	}
	_, err := db.Exec(`INSERT INTO import_cover_jobs(book_id,source,format,fingerprint) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM books WHERE id=?) ON CONFLICT(book_id) DO UPDATE SET source=excluded.source,format=excluded.format,attempts=0,retry_at=0,error='',state='pending',fingerprint=excluded.fingerprint,checked_at=0,version=import_cover_jobs.version+1`, id, source, format, sourceFingerprint(source), id)
	return err
}

// WorkOne holds no SQL transaction while extracting an image. Workers are
// deliberately bounded by the caller; retry data survives process restarts.
func WorkOne(ctx context.Context, db *sql.DB, root string) (bool, error) {
	return workOne(ctx, db, root, Ensure)
}

func workOne(ctx context.Context, db *sql.DB, root string, ensure func(context.Context, *sql.DB, string, int64, string, string) error) (bool, error) {
	var id int64
	var version int64
	var source, format string
	now := time.Now().Unix()
	err := db.QueryRowContext(ctx, `SELECT q.book_id FROM import_cover_jobs q INDEXED BY import_cover_ready JOIN books b ON b.id=q.book_id WHERE q.state<>'failed' AND q.retry_at<=? ORDER BY q.retry_at,q.book_id LIMIT 1`, now).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = db.QueryRowContext(ctx, `UPDATE import_cover_jobs SET retry_at=? WHERE book_id=? AND state<>'failed' AND retry_at<=? RETURNING book_id,source,format,version`, now+120, id, now).Scan(&id, &source, &format, &version)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = ensure(ctx, db, root, id, source, format); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		var attempts int
		if e := db.QueryRow(`SELECT attempts FROM import_cover_jobs WHERE book_id=? AND source=? AND version=?`, id, source, version).Scan(&attempts); e == sql.ErrNoRows {
			return true, nil
		} else if e != nil {
			return true, e
		}
		state := failureState(err, attempts+1)
		fp := sourceFingerprint(source)
		if fp == "unavailable" {
			state = "blocked"
		}
		increment := 1
		if state == "blocked" {
			increment = 0
		}
		delay := 30 * time.Minute * time.Duration(1<<min(attempts, 4))
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
		_, e := db.Exec(`UPDATE import_cover_jobs SET attempts=attempts+?,retry_at=?,error=?,state=?,fingerprint=?,checked_at=? WHERE book_id=? AND source=? AND version=?`, increment, time.Now().Add(delay).Unix(), err.Error(), state, fp, time.Now().Unix(), id, source, version)
		return true, e
	}
	_, err = db.Exec(`DELETE FROM import_cover_jobs WHERE book_id=? AND source=? AND version=?`, id, source, version)
	return true, err
}

func sourceFingerprint(source string) string {
	info, e := sourcefile.Stat(source)
	if e != nil || !info.Mode().IsRegular() {
		return "unavailable"
	}
	return sourcefile.FingerprintValues(source, info.Size(), info.ModTime().UnixNano())
}

func failureState(err error, attempts int) string {
	text := strings.ToLower(err.Error())
	if os.IsNotExist(err) || os.IsPermission(err) || strings.Contains(text, "database is locked") || strings.Contains(text, "sqlite_busy") || strings.Contains(text, "预算") || strings.Contains(text, "unavailable") || strings.Contains(text, "no such file") || strings.Contains(text, "permission denied") || strings.Contains(text, "input/output") {
		return "blocked"
	}
	for _, s := range []string{"invalid palmdb", "invalid mobi", "not a valid zip", "unknown image format", "image: unknown format", "malformed", "invalid epub"} {
		if strings.Contains(text, s) {
			return "failed"
		}
	}
	if attempts >= 5 && !errors.Is(err, context.Canceled) {
		return "failed"
	}
	return "retry"
}

// Recheck metadata only, never extract stopped files until they have changed.
func RecheckStopped(ctx context.Context, db *sql.DB, limit int) error {
	now := time.Now().Unix()
	rows, e := db.QueryContext(ctx, `SELECT book_id,source,fingerprint FROM import_cover_jobs INDEXED BY import_cover_recheck WHERE state IN ('failed','blocked') AND checked_at<=? ORDER BY checked_at,book_id LIMIT ?`, now-600, limit)
	if e != nil {
		return e
	}
	type job struct {
		id         int64
		source, fp string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if e = rows.Scan(&j.id, &j.source, &j.fp); e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if e = ctx.Err(); e != nil {
			return e
		}
		fp := sourceFingerprint(j.source)
		if fp != "unavailable" && fp != j.fp {
			_, e = db.ExecContext(ctx, `UPDATE import_cover_jobs SET state='pending',attempts=0,retry_at=0,error='',fingerprint=?,checked_at=?,version=version+1 WHERE book_id=? AND source=? AND fingerprint=?`, fp, now, j.id, j.source, j.fp)
		} else {
			_, e = db.ExecContext(ctx, `UPDATE import_cover_jobs SET checked_at=? WHERE book_id=? AND source=? AND fingerprint=?`, now, j.id, j.source, j.fp)
		}
		if e != nil {
			return e
		}
	}
	return nil
}

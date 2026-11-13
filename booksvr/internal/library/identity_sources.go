package library

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"talebook/booksvr/internal/sourcefile"
)

// Read indexed paths only. Neither this comparison nor discovery stats sources.
func identitySourceFamilies(q identityReader, id int64) ([]string, error) {
	rows, err := q.Query(`SELECT format,path FROM book_formats WHERE book_id=? ORDER BY format,path LIMIT 17`, id)
	if err != nil {
		var available bool
		if e := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='book_formats' AND type='table')`).Scan(&available); e == nil && !available {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	count := 0
	for rows.Next() {
		var format, stored string
		if err = rows.Scan(&format, &stored); err != nil {
			return nil, err
		}
		count++
		if count > 16 {
			return nil, nil
		}
		if stored == "" || sourcefile.IsReference(stored) {
			continue
		}
		ext := filepath.Ext(stored)
		if ext == "" || strings.TrimSuffix(filepath.Base(stored), ext) == "" || !strings.EqualFold(strings.TrimPrefix(ext, "."), format) {
			continue
		}
		stem := strings.TrimSuffix(stored, ext)
		if filepath.Base(stem) == "" || filepath.Base(stem) == "." {
			continue
		}
		set[filepath.Clean(stem)] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func hasIdentitySourceCompanion(q identityReader, id int64, title string) (bool, error) {
	families, err := identitySourceFamilies(q, id)
	if err != nil {
		return false, err
	}
	for _, family := range families {
		var found bool
		// The range has the exact "family." prefix in the BINARY path index.
		// Bound even rejected prefix neighbors; do not scan a directory or use
		// suffix/LIKE queries over the format table.
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM (SELECT book_id,format,path FROM book_formats INDEXED BY source_sync_format_path WHERE path>=? AND path<? ORDER BY path LIMIT 65) f JOIN book_identity_keys p ON p.book_id=f.book_id WHERE f.book_id<>? AND p.title_key=? AND f.format<>'' AND lower(substr(f.path,length(f.path)-length(f.format)))='.'||lower(f.format) AND substr(f.path,1,length(f.path)-length(f.format)-1)=?)`, family+".", family+"/", id, title, family).Scan(&found)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// A separate fixed-range cursor repairs old anonymous companions without
// resetting author projections, imports, metadata work or the regular seed.
func refreshIdentitySources(tx *sql.Tx) error {
	var ready, done int
	if err := tx.QueryRow(`SELECT coalesce((SELECT value FROM book_identity_state WHERE key='authors_v5_done'),0),coalesce((SELECT value FROM book_identity_state WHERE key='sources_v2_done'),0)`).Scan(&ready, &done); err != nil || ready == 0 || done != 0 {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO book_identity_state(key,value) VALUES('sources_v2_max',(SELECT coalesce(max(book_id),0) FROM book_identity_keys))`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT book_id FROM book_identity_keys WHERE book_id>coalesce((SELECT value FROM book_identity_state WHERE key='sources_v2_cursor'),0) AND book_id<=(SELECT value FROM book_identity_state WHERE key='sources_v2_max') ORDER BY book_id LIMIT 200`)
	if err != nil {
		return err
	}
	var ids []any
	var marks []string
	var last int64
	for rows.Next() {
		if err = rows.Scan(&last); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, last)
		marks = append(marks, "?")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		_, err = tx.Exec(`INSERT INTO book_identity_state(key,value) VALUES('sources_v2_done',1) ON CONFLICT(key) DO UPDATE SET value=1`)
		return err
	}
	rows, err = tx.Query(`SELECT k.book_id,k.title_key FROM book_identity_keys k WHERE k.book_id IN (`+strings.Join(marks, ",")+`) AND k.author_key='' AND k.title_key<>'' AND EXISTS(SELECT 1 FROM book_identity_keys p WHERE p.title_key=k.title_key AND p.book_id<>k.book_id)`, ids...)
	if err != nil {
		return err
	}
	type candidate struct {
		id    int64
		title string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.title); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		found, err := hasIdentitySourceCompanion(tx, c.id, c.title)
		if err != nil {
			return err
		}
		if found {
			if _, err = tx.Exec(`INSERT INTO book_identity_queue(book_id) VALUES(?) ON CONFLICT(book_id) DO UPDATE SET version=version+1,next_at=0`, c.id); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`INSERT INTO book_identity_state(key,value) VALUES('sources_v2_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, last)
	return err
}

package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"talebook/booksvr/internal/bookidentity"
)

type identityReader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type IdentityBook struct {
	ID, WorkID int64
	Metadata   bookidentity.Metadata
}
type IdentityResult struct {
	ID, WorkID int64
	Members    []int64
	Reason     string
	ack        map[int64]identityAck
}

type identityAck struct {
	version   int64
	signature string
	work      int64
}

var ErrIdentityAmbiguous = errors.New("identity candidates require review")
var ErrIdentityChanged = errors.New("book identity changed before format registration")

func AddIdentityFormat(db *sql.DB, id int64, m bookidentity.Metadata, format, path string, size, mtime int64) error {
	return addFormatGuarded(db, id, format, path, size, mtime, func(tx *sql.Tx) error {
		current, err := identityBook(tx, id)
		if err != nil {
			return err
		}
		if !bookidentity.Compare(m, current.Metadata).Edition || current.Metadata.ISBN == "!conflict" {
			return ErrIdentityChanged
		}
		return nil
	})
}

// Identity tables are additive. No source files, book IDs or user data move.
func EnsureIdentitySchema(db *sql.DB) error {
	for _, name := range []string{"works", "books", "identifiers", "authors", "books_authors_link", "publishers", "books_publishers_link", "languages", "books_languages_link"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS book_identity_keys(book_id INTEGER PRIMARY KEY,isbn TEXT NOT NULL,title_key TEXT NOT NULL,author_key TEXT NOT NULL,signature TEXT NOT NULL,managed_work INTEGER NOT NULL DEFAULT 0,reason TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS book_identity_isbn ON book_identity_keys(isbn,book_id) WHERE isbn<>''`,
		`CREATE INDEX IF NOT EXISTS book_identity_title ON book_identity_keys(title_key,author_key,book_id)`,
		`CREATE INDEX IF NOT EXISTS book_identity_group ON book_identity_keys(managed_work,book_id) WHERE managed_work>0`,
		`CREATE INDEX IF NOT EXISTS book_identity_raw_isbn ON identifiers(type,val,book)`,
		`CREATE TABLE IF NOT EXISTS book_identity_queue(book_id INTEGER PRIMARY KEY,version INTEGER NOT NULL DEFAULT 1,priority INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS book_identity_due ON book_identity_queue(priority DESC,book_id) WHERE next_at=0`,
		`CREATE INDEX IF NOT EXISTS book_identity_retry ON book_identity_queue(next_at,book_id) WHERE next_at>0`,
		`CREATE TABLE IF NOT EXISTS book_identity_state(key TEXT PRIMARY KEY,value INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS book_identity_lease(id INTEGER PRIMARY KEY CHECK(id=1),owner TEXT NOT NULL,expires INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS book_identity_locks(book_id INTEGER PRIMARY KEY)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	enqueue := func(ref string) string {
		return `INSERT INTO book_identity_queue(book_id,version,priority) VALUES(` + ref + `,1,10) ON CONFLICT(book_id) DO UPDATE SET version=version+1,priority=max(priority,10),next_at=0;`
	}
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS identity_book_insert AFTER INSERT ON books BEGIN ` + enqueue("NEW.id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS identity_book_update AFTER UPDATE OF title,author_sort,pubdate ON books BEGIN ` + enqueue("NEW.id") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS identity_book_delete AFTER DELETE ON books BEGIN DELETE FROM book_identity_keys WHERE book_id=OLD.id;DELETE FROM book_identity_queue WHERE book_id=OLD.id;DELETE FROM book_identity_locks WHERE book_id=OLD.id; END`,
	}
	for _, table := range []string{"identifiers", "books_authors_link", "books_publishers_link", "books_languages_link"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			body := ""
			if op != "DELETE" {
				body += enqueue("NEW.book")
			}
			if op != "INSERT" {
				body += enqueue("OLD.book")
			}
			triggers = append(triggers, `CREATE TRIGGER IF NOT EXISTS identity_`+table+`_`+strings.ToLower(op)+` AFTER `+op+` ON `+table+` BEGIN `+body+` END`)
		}
	}
	// Author rename is uncommon; this bounded index lookup enqueues affected books.
	triggers = append(triggers, `CREATE TRIGGER IF NOT EXISTS identity_author_update AFTER UPDATE OF name ON authors BEGIN INSERT INTO book_identity_queue(book_id,version,priority) SELECT book,1,10 FROM books_authors_link WHERE author=NEW.id AND true ON CONFLICT(book_id) DO UPDATE SET version=version+1,priority=max(priority,10),next_at=0; END`)
	var hasFormats bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='book_formats' AND type='table')`).Scan(&hasFormats); err != nil {
		return err
	}
	if hasFormats {
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS source_sync_format_path ON book_formats(path)`); err != nil {
			return err
		}
		triggers = append(triggers,
			`CREATE TRIGGER IF NOT EXISTS identity_format_insert AFTER INSERT ON book_formats BEGIN `+enqueue("NEW.book_id")+` END`,
			`CREATE TRIGGER IF NOT EXISTS identity_format_update AFTER UPDATE OF path,format,book_id ON book_formats BEGIN `+enqueue("NEW.book_id")+enqueue("OLD.book_id")+` END`,
			`CREATE TRIGGER IF NOT EXISTS identity_format_delete AFTER DELETE ON book_formats BEGIN `+enqueue("OLD.book_id")+` END`)
	}
	for _, q := range triggers {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
func IdentityAvailable(db *sql.DB) bool {
	if db == nil {
		return false
	}
	var n int
	return db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='book_identity_keys' AND type='table'`).Scan(&n) == nil && n > 0
}
func identityBook(q identityReader, id int64) (IdentityBook, error) {
	b := IdentityBook{ID: id}
	var fallback string
	err := q.QueryRow(`SELECT title,author_sort,coalesce(pubdate,''),coalesce(work_id,0) FROM books WHERE id=?`, id).Scan(&b.Metadata.Title, &fallback, &b.Metadata.Pubdate, &b.WorkID)
	if err != nil {
		return b, err
	}
	rows, err := q.Query(`SELECT a.name FROM books_authors_link l JOIN authors a ON a.id=l.author WHERE l.book=? ORDER BY l.id`, id)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			rows.Close()
			return b, err
		}
		b.Metadata.Authors = append(b.Metadata.Authors, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if len(b.Metadata.Authors) == 0 && fallback != "" {
		b.Metadata.Authors = []string{fallback}
	}
	rows, err = q.Query(`SELECT val FROM identifiers WHERE book=? AND type='isbn' ORDER BY id`, id)
	if err != nil {
		return b, err
	}
	canonical := ""
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			rows.Close()
			return b, err
		}
		if key := bookidentity.ISBN(s); key != "" {
			if canonical != "" && key != canonical {
				canonical = "!conflict"
				break
			}
			canonical = key
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	b.Metadata.ISBN = canonical
	for _, attr := range []struct {
		query string
		out   *string
	}{
		{`SELECT p.name FROM books_publishers_link l JOIN publishers p ON p.id=l.publisher WHERE l.book=? ORDER BY l.id LIMIT 1`, &b.Metadata.Publisher},
		{`SELECT p.lang_code FROM books_languages_link l JOIN languages p ON p.id=l.lang_code WHERE l.book=? ORDER BY l.id LIMIT 1`, &b.Metadata.Language},
	} {
		err = q.QueryRow(attr.query, id).Scan(attr.out)
		if err != nil && err != sql.ErrNoRows {
			return b, err
		}
	}
	b.Metadata.SourceFamilies, err = identitySourceFamilies(q, id)
	return b, err
}
func identitySignature(m bookidentity.Metadata) string {
	data, _ := json.Marshal(struct {
		Metadata bookidentity.Metadata
		Sources  []string
	}{m, m.SourceFamilies})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Candidates use indexed equality lookups, not LIKE or a full-library pass.
func identityCandidates(q identityReader, m bookidentity.Metadata) ([]int64, error) {
	k := bookidentity.Key(m)
	var projected bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='book_identity_keys')`).Scan(&projected); err != nil {
		return nil, err
	}
	set := map[int64]bool{}
	strong := map[int64]bool{}
	queries := []struct {
		sql  string
		args []any
	}{}
	if projected && k.Title != "" {
		queries = append(queries, struct {
			sql  string
			args []any
		}{`SELECT book_id FROM book_identity_keys WHERE title_key=? AND author_key=? ORDER BY book_id LIMIT 65`, []any{k.Title, k.Authors}})
	}
	queries = append(queries, struct {
		sql  string
		args []any
	}{`SELECT id FROM books WHERE title=? ORDER BY id LIMIT 65`, []any{m.Title}})
	if k.ISBN != "" {
		if projected {
			queries = append(queries, struct {
				sql  string
				args []any
			}{`SELECT book_id FROM book_identity_keys WHERE isbn=? AND isbn<>'' ORDER BY book_id LIMIT 65`, []any{k.ISBN}})
		}
		forms := bookidentity.ISBNForms(m.ISBN)
		marks := []string{}
		args := []any{}
		for _, v := range forms {
			marks = append(marks, "?")
			args = append(args, v)
		}
		queries = append(queries, struct {
			sql  string
			args []any
		}{`SELECT book FROM identifiers WHERE type='isbn' AND val IN (` + strings.Join(marks, ",") + `) ORDER BY book LIMIT 65`, args})
	}
	for _, query := range queries {
		rows, err := q.Query(query.sql, query.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			set[id] = true
			if strings.Contains(query.sql, "WHERE isbn=") || strings.Contains(query.sql, "type='isbn'") {
				strong[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(set) > 64 && len(strong) > 0 && len(strong) <= 64 {
		set = strong
	}
	if len(set) > 64 {
		return nil, fmt.Errorf("%w: 候选超过 64 条，保留各版本等待核对", ErrIdentityAmbiguous)
	}
	out := []int64{}
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// MatchIdentityBooks returns compatible editions, preferring ISBN evidence.
// Missing format slots may be filled; occupied slots must remain separate files.
func MatchIdentityBooks(db *sql.DB, m bookidentity.Metadata) ([]int64, error) {
	ids, err := identityCandidates(db, m)
	if errors.Is(err, ErrIdentityAmbiguous) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	matched := []IdentityBook{}
	strong := false
	for _, id := range ids {
		var locked bool
		if e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM book_identity_locks WHERE book_id=?)`, id).Scan(&locked); e != nil {
			return nil, e
		}
		if locked {
			continue
		}
		b, e := identityBook(db, id)
		if e != nil {
			return nil, e
		}
		d := bookidentity.Compare(m, b.Metadata)
		if !d.Edition || b.Metadata.ISBN == "!conflict" {
			continue
		}
		isStrong := bookidentity.ISBN(m.ISBN) != "" && bookidentity.ISBN(m.ISBN) == bookidentity.ISBN(b.Metadata.ISBN)
		if isStrong && !strong {
			matched = nil
			strong = true
		}
		if !strong || isStrong {
			matched = append(matched, b)
		}
	}
	// Do not let an author-less ISBN entry bridge conflicting coauthor groups.
	for i := range matched {
		for j := 0; j < i; j++ {
			if !bookidentity.Compare(matched[i].Metadata, matched[j].Metadata).Edition {
				return nil, nil
			}
		}
	}
	out := []int64{}
	for _, b := range matched {
		out = append(out, b.ID)
	}
	return out, nil
}
func saveIdentityKey(tx *sql.Tx, b IdentityBook, managed int64, reason string) error {
	k := bookidentity.Key(b.Metadata)
	_, err := tx.Exec(`INSERT INTO book_identity_keys(book_id,isbn,title_key,author_key,signature,managed_work,reason,updated_at) VALUES(?,?,?,?,?,?,?,unixepoch()) ON CONFLICT(book_id) DO UPDATE SET isbn=excluded.isbn,title_key=excluded.title_key,author_key=excluded.author_key,signature=excluded.signature,managed_work=excluded.managed_work,reason=excluded.reason,updated_at=excluded.updated_at`, b.ID, k.ISBN, k.Title, k.Authors, identitySignature(b.Metadata), managed, reason)
	return err
}
func identityProtected(q identityReader, b IdentityBook) (bool, error) {
	var available bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='book_identity_locks')`).Scan(&available); err != nil {
		return false, err
	}
	if !available {
		return b.WorkID > 0, nil
	}
	var locked bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM book_identity_locks WHERE book_id=?)`, b.ID).Scan(&locked); err != nil {
		return false, err
	}
	if locked {
		return true, nil
	}
	if b.WorkID == 0 {
		return false, nil
	}
	var managed int64
	err := q.QueryRow(`SELECT managed_work FROM book_identity_keys WHERE book_id=?`, b.ID).Scan(&managed)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	return managed != b.WorkID, nil
}

// ReconcileBookIdentity only writes grouping/index fields, never metadata or files.
func ReconcileBookIdentity(db *sql.DB, id int64, dry bool) (IdentityResult, error) {
	result := IdentityResult{ID: id}
	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if !dry {
		if _, err = tx.Exec(`UPDATE books SET id=id WHERE id=?`, id); err != nil {
			return result, err
		}
	}
	current, err := identityBook(tx, id)
	if err != nil {
		return result, err
	}
	protected, err := identityProtected(tx, current)
	if err != nil {
		return result, err
	}
	if protected {
		result.WorkID = current.WorkID
		result.Reason = "manual_group"
		return result, nil
	}
	members, err := identityNeighborhood(tx, current)
	if errors.Is(err, ErrIdentityAmbiguous) {
		result.Reason = "too_many_candidates"
		if !dry {
			if e := saveIdentityKey(tx, current, current.WorkID, result.Reason); e != nil {
				return result, e
			}
			err = tx.Commit()
		} else {
			err = nil
		}
		return result, err
	}
	if err != nil {
		return result, err
	}
	neighborhood := members
	reason := "single"
	for _, b := range members[1:] {
		if d := bookidentity.Compare(current.Metadata, b.Metadata); d.Work {
			reason = d.Reason
		}
	}
	metadata := make([]bookidentity.Metadata, 0, len(members))
	for _, member := range members {
		metadata = append(metadata, member.Metadata)
	}
	compatible := current.Metadata.ISBN != "!conflict" && bookidentity.GroupCompatible(metadata)
	if !compatible && current.Metadata.ISBN != "!conflict" {
		groups := make([]int64, len(members))
		for i, member := range members {
			groups[i] = member.WorkID
		}
		if subset := bookidentity.CompatibleSubset(metadata, groups, 0); len(subset) > 1 {
			selected := make([]IdentityBook, 0, len(subset))
			for _, i := range subset {
				selected = append(selected, members[i])
			}
			members = selected
			compatible = true
			reason = "compatible_subset"
		}
	}
	if !compatible {
		members = []IdentityBook{current}
		reason = "conflicting_candidates"
	}
	for _, b := range members {
		result.Members = append(result.Members, b.ID)
	}
	sort.Slice(result.Members, func(i, j int) bool { return result.Members[i] < result.Members[j] })
	result.Reason = reason
	if dry {
		return result, nil
	}
	// Most books have no sibling. Index them without manufacturing a work row
	// for every book in a large library. Detach a stale automatic association.
	if len(members) == 1 {
		managed := current.WorkID
		if !compatible {
			managed = 0
			if _, err = tx.Exec(`UPDATE books SET work_id=NULL WHERE id=?`, id); err != nil {
				return result, err
			}
			// Detaching an ambiguous old member can unblock neighbors already
			// processed while that automatic group was still an atomic unit.
			// Version the follow-up events inside this transaction so an older
			// worker acknowledgement cannot consume them. Unmanaged ambiguous
			// records do not requeue themselves indefinitely.
			if current.WorkID > 0 {
				for _, neighbor := range neighborhood {
					if _, err = tx.Exec(`INSERT INTO book_identity_queue(book_id) VALUES(?) ON CONFLICT(book_id) DO UPDATE SET version=version+1,next_at=0`, neighbor.ID); err != nil {
						return result, err
					}
				}
			}
		}
		if err = saveIdentityKey(tx, current, managed, reason); err != nil {
			return result, err
		}
		result.WorkID = managed
		return result, tx.Commit()
	}
	work := int64(0)
	if compatible {
		for _, b := range members {
			if b.WorkID > 0 && (work == 0 || b.WorkID < work) {
				work = b.WorkID
			}
		}
	}
	if work == 0 {
		key := fmt.Sprintf("identity:%d:%s", id, identitySignature(current.Metadata))
		if _, err = tx.Exec(`INSERT OR IGNORE INTO works(title_norm,author_norm,work_key,display_title,display_author,created_at,updated_at) VALUES(?,?,?,?,?,unixepoch(),unixepoch())`, bookidentity.Key(current.Metadata).Title, bookidentity.Key(current.Metadata).Authors, key, bookidentity.CleanTitle(current.Metadata.Title), strings.Join(current.Metadata.Authors, " & ")); err != nil {
			return result, err
		}
		if err = tx.QueryRow(`SELECT id FROM works WHERE work_key=?`, key).Scan(&work); err != nil {
			return result, err
		}
	}
	marks := make([]string, 0, len(members))
	args := make([]any, 0, len(members))
	signatures := map[int64]string{}
	for _, b := range members {
		marks = append(marks, "?")
		args = append(args, b.ID)
		signatures[b.ID] = identitySignature(b.Metadata)
	}
	rows, e := tx.Query(`SELECT book_id,version FROM book_identity_queue WHERE book_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if e != nil {
		return result, e
	}
	result.ack = map[int64]identityAck{}
	for rows.Next() {
		var id, version int64
		if e = rows.Scan(&id, &version); e != nil {
			rows.Close()
			return result, e
		}
		result.ack[id] = identityAck{version, signatures[id], work}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	// Generations are captured before our writes can fire metadata-change triggers.
	for _, b := range members {
		k := bookidentity.Key(b.Metadata)
		if _, err = tx.Exec(`UPDATE books SET work_id=?,title_norm=?,author_norm=? WHERE id=?`, work, k.Title, k.Authors, b.ID); err != nil {
			return result, err
		}
		if err = saveIdentityKey(tx, b, work, reason); err != nil {
			return result, err
		}
	}
	result.WorkID = work
	return result, tx.Commit()
}

func acknowledgeIdentity(db *sql.DB, id, version int64, result IdentityResult) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, captured := result.ack[id]; !captured {
		if _, err = tx.Exec(`DELETE FROM book_identity_queue WHERE book_id=? AND version=?`, id, version); err != nil {
			return err
		}
	}
	for member, proof := range result.ack {
		if _, err = tx.Exec(`DELETE FROM book_identity_queue WHERE book_id=? AND version=? AND EXISTS(SELECT 1 FROM book_identity_keys k JOIN books b ON b.id=k.book_id WHERE k.book_id=? AND k.signature=? AND k.managed_work=? AND b.work_id=k.managed_work)`, member, proof.version, member, proof.signature, proof.work); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IdentityVersions is bounded and read-only; HTTP must filter each ID by visibility.
func IdentityVersions(db *sql.DB, id int64) ([]int64, error) {
	if !IdentityAvailable(db) {
		return nil, nil
	}
	rows, err := db.Query(`SELECT b.id FROM books b WHERE b.work_id=(SELECT work_id FROM books WHERE id=?) AND b.id<>? ORDER BY b.id LIMIT 64`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			return nil, err
		}
		ids = append(ids, n)
	}
	return ids, rows.Err()
}

// LockIdentity preserves explicit grouping and splitting in their transaction.
func LockIdentity(tx *sql.Tx, ids []int64) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='book_identity_locks'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO book_identity_locks(book_id) VALUES(?)`, id); err != nil {
			return err
		}
	}
	return nil
}

// RunIdentityBatch uses an exclusive short lease and bounded, resumable discovery.
func RunIdentityBatch(ctx context.Context, db *sql.DB, owner string, limit int) (int, error) {
	if limit < 1 || limit > 50 {
		limit = 10
	}
	r, err := db.ExecContext(ctx, `INSERT INTO book_identity_lease(id,owner,expires) VALUES(1,?,unixepoch()+60) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE expires<unixepoch()`, owner)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return 0, nil
	}
	defer db.Exec(`DELETE FROM book_identity_lease WHERE id=1 AND owner=?`, owner)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	// Acquire the writer before reading a snapshot. Source synchronization and
	// metadata updates may otherwise commit between the read and its upgrade.
	if _, err = tx.ExecContext(ctx, `UPDATE book_identity_state SET value=value WHERE key='cursor'`); err != nil {
		tx.Rollback()
		return 0, err
	}
	if err = refreshIdentityAuthors(tx); err != nil {
		tx.Rollback()
		return 0, err
	}
	if err = refreshIdentitySources(tx); err != nil {
		tx.Rollback()
		return 0, err
	}
	var cursor int64
	err = tx.QueryRow(`SELECT value FROM book_identity_state WHERE key='cursor'`).Scan(&cursor)
	if err != nil && err != sql.ErrNoRows {
		tx.Rollback()
		return 0, err
	}
	// The primary-key cursor never restarts a full scan after process restarts.
	rows, err := tx.Query(`SELECT id FROM books WHERE id>? ORDER BY id LIMIT 100`, cursor)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	seed := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			tx.Rollback()
			return 0, err
		}
		seed = append(seed, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	for _, id := range seed {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO book_identity_queue(book_id) VALUES(?)`, id); err != nil {
			tx.Rollback()
			return 0, err
		}
		cursor = id
	}
	if _, err = tx.Exec(`INSERT INTO book_identity_state(key,value) VALUES('cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cursor); err != nil {
		tx.Rollback()
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	rows, err = db.QueryContext(ctx, `SELECT book_id,version FROM book_identity_queue WHERE next_at=0 ORDER BY priority DESC,book_id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type item struct{ id, version int64 }
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.version); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(items) < limit {
		retryRows, e := db.QueryContext(ctx, `SELECT book_id,version FROM book_identity_queue WHERE next_at>0 AND next_at<=unixepoch() ORDER BY next_at,book_id LIMIT ?`, limit-len(items))
		if e != nil {
			return 0, e
		}
		for retryRows.Next() {
			var i item
			if e = retryRows.Scan(&i.id, &i.version); e != nil {
				retryRows.Close()
				return 0, e
			}
			items = append(items, i)
		}
		e = retryRows.Err()
		retryRows.Close()
		if e != nil {
			return 0, e
		}
	}
	done := 0
	for _, i := range items {
		if err = ctx.Err(); err != nil {
			return done, err
		}
		var currentVersion int64
		if e := db.QueryRowContext(ctx, `SELECT version FROM book_identity_queue WHERE book_id=?`, i.id).Scan(&currentVersion); e == sql.ErrNoRows || (e == nil && currentVersion != i.version) {
			continue
		} else if e != nil {
			return done, e
		}
		renewed, e := db.ExecContext(ctx, `UPDATE book_identity_lease SET expires=unixepoch()+60 WHERE id=1 AND owner=? AND expires>=unixepoch()`, owner)
		if e != nil {
			return done, e
		}
		held, _ := renewed.RowsAffected()
		if held != 1 {
			return done, nil
		}
		result, e := ReconcileBookIdentity(db, i.id, false)
		if e == nil || errors.Is(e, sql.ErrNoRows) {
			err = acknowledgeIdentity(db, i.id, i.version, result)
			done++
		} else {
			_, err = db.ExecContext(ctx, `UPDATE book_identity_queue SET next_at=unixepoch()+30,last_error=? WHERE book_id=? AND version=?`, e.Error(), i.id, i.version)
		}
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

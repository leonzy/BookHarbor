package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"talebook/booksvr/internal/booktools"
	"talebook/booksvr/internal/importmeta"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/scan"
	"talebook/booksvr/internal/sourcefile"
)

type metadataWorker struct {
	w            *bookWiring
	owner        string
	now          func() time.Time
	logger       *log.Logger
	active       map[int64]bool
	results      chan sourceResult
	providerTurn int
	lastRetry    time.Time
}

func startMetadataWorker(ctx context.Context, opts ServeOptions, db, userDB *sql.DB) (func(), error) {
	if err := library.EnsureMetadataSchema(db); err != nil {
		return nil, err
	}
	w := newBookWiring(userDB, db, opts.Settings, opts.PluginRegistry).withSettingsPath(opts.SettingsPath)
	w.libraryRoot = opts.LibraryRoot
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	worker := &metadataWorker{w: w, owner: hex.EncodeToString(token), now: time.Now, logger: log.New(out, "metadata: ", log.LstdFlags)}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	networkDone := make(chan struct{})
	go func() {
		defer close(networkDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				if ok, e := worker.lease(); e != nil {
					worker.logger.Printf("联网调度暂缓: %v", e)
				} else if ok {
					if e = worker.network(run); e != nil {
						worker.logger.Printf("联网补全暂缓: %v", e)
					}
				}
			}
		}
	}()
	go func() {
		defer close(done)
		ticker := time.NewTimer(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				if err := worker.step(run); err != nil {
					worker.logger.Printf("补全暂缓: %v", err)
				}
				ticker.Reset(time.Second)
			}
		}
	}()
	return func() {
		cancel()
		<-done
		<-networkDone
		for len(worker.active) > 0 {
			r := <-worker.results
			delete(worker.active, r.task.connection)
			if e := worker.coolSource(r.task.connection); e != nil {
				worker.logger.Printf("来源冷却保存失败: %v", e)
			}
		}
		_, _ = db.Exec(`DELETE FROM metadata_enrichment_lease WHERE id=1 AND owner=?`, worker.owner)
	}, nil
}

func (m *metadataWorker) state(key string) (int64, error) {
	var n int64
	err := m.w.libraryDB.QueryRow(`SELECT value FROM metadata_enrichment_state WHERE key=?`, key).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}
func (m *metadataWorker) setState(key string, n int64) error {
	_, err := m.w.libraryDB.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, n)
	return err
}

func (m *metadataWorker) lease() (bool, error) {
	now := m.now().Unix()
	r, err := m.w.libraryDB.Exec(`INSERT INTO metadata_enrichment_lease(id,owner,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE metadata_enrichment_lease.owner=excluded.owner OR metadata_enrichment_lease.expires<?`, m.owner, now+120, now)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// nextBook uses primary-key cursors, prioritising arrivals over the historical pass.
// It never loads the library or all source paths into memory.
func (m *metadataWorker) nextBook() (id int64, cursor string, err error) {
	db := m.w.libraryDB
	var initialized int
	if err = db.QueryRow(`SELECT count(*) FROM metadata_enrichment_state WHERE key='initial_max'`).Scan(&initialized); err != nil {
		return
	}
	if initialized == 0 {
		var max int64
		if err = db.QueryRow(`SELECT coalesce(max(id),0) FROM books`).Scan(&max); err != nil {
			return
		}
		if err = m.setState("initial_max", max); err != nil {
			return
		}
		if err = m.setState("new_cursor", max); err != nil {
			return
		}
	}
	high, e := m.state("new_cursor")
	if e != nil {
		err = e
		return
	}
	err = db.QueryRow(`SELECT id FROM books WHERE id>? ORDER BY id LIMIT 1`, high).Scan(&id)
	if err == nil {
		return id, "new_cursor", nil
	}
	if err != sql.ErrNoRows {
		return
	}
	old, e := m.state("history_cursor")
	if e != nil {
		err = e
		return
	}
	max, e := m.state("initial_max")
	if e != nil {
		err = e
		return
	}
	err = db.QueryRow(`SELECT id FROM books WHERE id>? AND id<=? ORDER BY id LIMIT 1`, old, max).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	return id, "history_cursor", err
}

func (m *metadataWorker) saveJob(id int64, status, reason string, delay time.Duration) error {
	// Store bounded diagnostic text without paths, provider URLs, or credentials.
	if len(reason) > 240 {
		reason = reason[:240]
	}
	_, err := m.w.libraryDB.Exec(`INSERT INTO metadata_enrichment_jobs(book_id,status,next_at,last_error,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(book_id) DO UPDATE SET status=excluded.status,next_at=excluded.next_at,last_error=excluded.last_error,updated_at=excluded.updated_at`, id, status, m.now().Add(delay).Unix(), reason, m.now().Unix())
	if err == nil && (status == "network_pending" || status == "partial") {
		_, err = m.w.libraryDB.Exec(`INSERT INTO metadata_source_pending(book_id) VALUES(?) ON CONFLICT(book_id) DO UPDATE SET version=version+1`, id)
	}
	return err
}

func (m *metadataWorker) local(ctx context.Context, id int64, force ...bool) error {
	// Skip already completed records when an interrupted checkpoint is replayed.
	var existing string
	err := m.w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=?`, id).Scan(&existing)
	if err == nil && existing != "file_retry" && !(len(force) > 0 && force[0]) {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		if err = library.ProtectHistoricalMetadataEdits(m.w.libraryDB, id); err != nil {
			return err
		}
	}
	var protected int
	if err = m.w.libraryDB.QueryRow(`SELECT count(*) FROM metadata_field_locks WHERE book_id=? AND field IN ('title','authors','publisher','isbn','language','pubdate','comments','tags')`, id).Scan(&protected); err != nil {
		return err
	}
	if protected == 8 {
		return m.saveJob(id, "protected", "历史修改或人工编辑保护，保留现有值和明确清空的字段", 0)
	}
	rows, err := m.w.libraryDB.Query(`SELECT format FROM data WHERE book=? ORDER BY CASE upper(format) WHEN 'EPUB' THEN 0 WHEN 'AZW3' THEN 1 WHEN 'MOBI' THEN 2 WHEN 'PDF' THEN 4 ELSE 3 END LIMIT 4`, id)
	if err != nil {
		return err
	}
	var formats []string
	for rows.Next() {
		var f string
		if err = rows.Scan(&f); err != nil {
			rows.Close()
			return err
		}
		formats = append(formats, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var found importmeta.Metadata
	readable := false
	for _, format := range formats {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, e := booktools.GetFormatPath(m.w.libraryDB, id, format)
		if e != nil {
			continue
		}
		if !sourcefile.IsReference(path) && !filepath.IsAbs(path) {
			path = filepath.Join(m.w.libraryRoot, path)
		}
		if sourcefile.IsReference(path) {
			ref, e := sourcefile.Decode(path)
			if e != nil || ref.Size > 32<<20 {
				continue
			}
		}
		info, e := sourcefile.Stat(path)
		if e != nil || info.IsDir() {
			continue
		}
		metadata, e := scan.ReadMetadata(ctx, path, format)
		if e != nil {
			continue
		}
		readable = true
		localmeta.Merge(&found, metadata)
		if found.ISBN != "" && found.Publisher != "" && !localmeta.MissingAuthors(found.Authors) {
			break
		}
	}
	if !readable {
		return m.saveJob(id, "file_retry", "来源文件暂不可读、未挂载或超过 ZIP 临时读取预算，未修改来源状态", time.Hour)
	}
	changed, err := library.FillMissingMetadata(m.w.libraryDB, id, found, "file")
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	identityChanged := false
	for _, field := range changed {
		if field == "isbn" || field == "authors" {
			identityChanged = true
		}
	}
	if len(changed) > 0 {
		if identityChanged {
			if _, err = m.w.libraryDB.Exec(`DELETE FROM metadata_source_jobs WHERE book_id=? AND phase='done'`, id); err != nil {
				return err
			}
		}
		m.logger.Printf("book=%d 文件补全: %s", id, strings.Join(changed, ","))
	}
	books, err := library.GetBooksData(m.w.libraryDB, []int64{id})
	if err != nil {
		return err
	}
	if len(books) == 0 {
		return nil
	}
	b := books[0]
	status := "network_pending"
	if localmeta.ISBN(b.ISBN) != "" && b.Publisher != "" && !localmeta.MissingAuthors(b.Authors) && b.Comments != "" {
		status = "complete"
	}
	if status != "complete" && !identityChanged && (existing == "partial" || existing == "needs_review") {
		return nil
	}
	return m.saveJob(id, status, "", 0)
}

func (m *metadataWorker) step(ctx context.Context) error {
	ok, err := m.lease()
	if err != nil || !ok {
		return err
	}
	id, cursor, err := m.nextBook()
	if err != nil {
		return err
	}
	if id > 0 {
		if err = m.local(ctx, id); err != nil {
			return err
		}
		if err = m.setState(cursor, id); err != nil {
			return err
		}
	}
	now := m.now()
	if now.Sub(m.lastRetry) >= time.Minute {
		m.lastRetry = now
		var retry int64
		err = m.w.libraryDB.QueryRow(`SELECT book_id FROM metadata_enrichment_jobs WHERE status='file_retry' AND next_at<=? ORDER BY next_at,book_id LIMIT 1`, now.Unix()).Scan(&retry)
		if err == nil {
			if err = m.local(ctx, retry); err != nil {
				return err
			}
		} else if err != sql.ErrNoRows {
			return err
		}
	}

	return nil
}

func normalizeMetadataName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
func isbn13(s string) string {
	s = localmeta.ISBN(s)
	if len(s) != 10 {
		return s
	}
	prefix := "978" + s[:9]
	sum := 0
	for i := 0; i < 12; i++ {
		v := int(prefix[i] - '0')
		if i%2 == 1 {
			v *= 3
		}
		sum += v
	}
	return fmt.Sprintf("%s%d", prefix, (10-sum%10)%10)
}

// Strong matching prevents automatically assigning metadata from a namesake.
func matchingMetadata(book library.BookData, candidates []map[string]any) map[string]any {
	var selected map[string]any
	for _, candidate := range candidates {
		if !referenceRelevant(book, candidate) {
			continue
		}
		isbn := isbn13(stringOf(candidate["isbn"]))
		current := isbn13(book.ISBN)
		if current != "" && isbn != "" && current != isbn {
			continue
		}
		if current != "" && isbn == "" {
			continue
		}
		match := current != "" && current == isbn
		if !match && normalizeMetadataName(localmeta.FilenameTitle(book.Title)) == normalizeMetadataName(localmeta.FilenameTitle(stringOf(candidate["title"]))) && !localmeta.MissingAuthors(book.Authors) {
			for _, a := range book.Authors {
				if localmeta.UnknownAuthor(a) {
					continue
				}
				for _, b := range toStringSlice(candidate["authors"]) {
					if !localmeta.UnknownAuthor(b) && metadataAuthor(a) == metadataAuthor(b) {
						match = true
					}
				}
			}
		}
		if !match {
			continue
		}
		if selected != nil {
			// Same-title searches returning multiple editions require a human decision.
			if current == "" && (isbn13(stringOf(selected["isbn"])) != isbn || stringOf(selected["provider_value"]) != stringOf(candidate["provider_value"])) {
				return nil
			}
		} else {
			selected = candidate
		}
	}
	return selected
}

func metadataCandidate(record map[string]any) importmeta.Metadata {
	return importmeta.Metadata{Authors: toStringSlice(record["authors"]), Publisher: stringOf(record["publisher"]), ISBN: localmeta.ISBN(stringOf(record["isbn"])), Language: stringOf(record["language"]), Pubdate: stringOf(record["pubdate"]), Comments: stringOf(record["comments"]), Tags: toStringSlice(record["tags"])}
}

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"talebook/booksvr/internal/pluginproto"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginruntime"
	"talebook/booksvr/internal/userdb"
)

const metadataSourceConcurrency = 8

type sourceTask struct {
	book, connection int64
	phase, record    string
	attempts         int
}
type sourceResult struct {
	task  sourceTask
	value any
	err   error
}

func (m *metadataWorker) sourceConnections(settings map[string]any) ([]*userdb.PluginConnection, error) {
	rt := pluginruntime.NewRuntime(m.w.db, settings, m.w.registry)
	conns, e := rt.ConnectionsFor(metadataLookupCapability, nil)
	if e != nil {
		return nil, e
	}
	rows, e := m.w.db.Query(`SELECT DISTINCT owner_id FROM plugin_connections WHERE enabled=1 AND owner_type='user'`)
	if e != nil {
		return nil, e
	}
	var users []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		users = append(users, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	seen := map[int64]bool{}
	for _, c := range conns {
		seen[c.ID] = true
	}
	for _, id := range users {
		more, e := rt.ConnectionsFor(metadataLookupCapability, &id)
		if e != nil {
			return nil, e
		}
		for _, c := range more {
			if !seen[c.ID] {
				conns = append(conns, c)
				seen[c.ID] = true
			}
		}
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].ID < conns[j].ID })
	return conns, nil
}

func (m *metadataWorker) sourceUnavailable(rt *pluginruntime.Runtime, c *userdb.PluginConnection) string {
	key, e := rt.PluginKeyOf(c)
	if e != nil {
		return "unavailable"
	}
	if key == "talebook.meta.calibre" {
		return "not_connected"
	}
	if key == "talebook.source.legado" {
		var n int
		if e = m.w.db.QueryRow(`SELECT count(*) FROM book_sources WHERE enabled=1 AND source_type=0`).Scan(&n); e != nil || n == 0 {
			return "no_sources"
		}
	}
	return ""
}

func metadataSourceOwners(db *sql.DB, ids []int64) (map[int64]int64, error) {
	owners := map[int64]int64{}
	if len(ids) == 0 {
		return owners, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	rows, e := db.Query(`SELECT book_id,coalesce(collector_id,0) FROM items WHERE book_id IN (`+strings.Join(marks, ",")+`) ORDER BY book_id,collector_id`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, user int64
		if e = rows.Scan(&id, &user); e != nil {
			return nil, e
		}
		if _, seen := owners[id]; !seen {
			owners[id] = user
		}
	}
	return owners, rows.Err()
}

// Enrollment is bounded. The cursor indexes the existing checked-book queue,
// never the source filesystem. A local recheck also leaves an explicit event.
func (m *metadataWorker) enrollSources(conns []*userdb.PluginConnection) error {
	db := m.w.libraryDB
	var high int64
	e := db.QueryRow(`SELECT value FROM metadata_enrichment_state WHERE key='initial_max'`).Scan(&high)
	if e == sql.ErrNoRows {
		e = db.QueryRow(`SELECT coalesce(max(id),0) FROM books`).Scan(&high)
	}
	if e != nil {
		return e
	}
	for _, c := range conns {
		if _, e := db.Exec(`INSERT OR IGNORE INTO metadata_source_limits(connection_id) VALUES(?)`, c.ID); e != nil {
			return e
		}
		var cursor int64
		if e := db.QueryRow(`SELECT seed_cursor FROM metadata_source_limits WHERE connection_id=?`, c.ID).Scan(&cursor); e != nil {
			return e
		}
		rows, e := db.Query(`SELECT book_id FROM metadata_enrichment_jobs WHERE book_id>? AND status IN ('network_pending','partial') ORDER BY book_id LIMIT 100`, cursor)
		if e != nil {
			return e
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) > 0 {
			owners, e := metadataSourceOwners(m.w.db, ids)
			if e != nil {
				return e
			}
			tx, e := db.Begin()
			if e != nil {
				return e
			}
			for _, id := range ids {
				if owners[id] <= 0 {
					_, e = tx.Exec(`UPDATE metadata_enrichment_jobs SET status='needs_review',last_error='书籍没有可用的所属用户' WHERE book_id=?`, id)
					if e != nil {
						break
					}
					continue
				}
				if c.OwnerType == "user" && c.OwnerID != owners[id] {
					continue
				}
				_, e = tx.Exec(`INSERT OR IGNORE INTO metadata_source_jobs(book_id,connection_id,priority) VALUES(?,?,?)`, id, c.ID, sourceBookPriority(id, high))
				if e != nil {
					break
				}
			}
			if e == nil {
				_, e = tx.Exec(`UPDATE metadata_source_limits SET seed_cursor=? WHERE connection_id=?`, ids[len(ids)-1], c.ID)
			}
			if e != nil {
				tx.Rollback()
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
		}
	}
	if len(conns) == 0 {
		return nil
	}
	rows, e := db.Query(`SELECT book_id,version FROM metadata_source_pending ORDER BY book_id LIMIT 100`)
	if e != nil {
		return e
	}
	var ids []int64
	versions := map[int64]int64{}
	for rows.Next() {
		var id, version int64
		if e = rows.Scan(&id, &version); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
		versions[id] = version
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return nil
	}
	owners, e := metadataSourceOwners(m.w.db, ids)
	if e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, id := range ids {
		if owners[id] <= 0 {
			if _, e = tx.Exec(`UPDATE metadata_enrichment_jobs SET status='needs_review',last_error='书籍没有可用的所属用户' WHERE book_id=?`, id); e != nil {
				return e
			}
		}
		for _, c := range conns {
			if owners[id] <= 0 || (c.OwnerType == "user" && c.OwnerID != owners[id]) {
				continue
			}
			if _, e = tx.Exec(`INSERT OR IGNORE INTO metadata_source_jobs(book_id,connection_id,priority) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM metadata_enrichment_jobs WHERE book_id=? AND status IN ('network_pending','partial'))`, id, c.ID, sourceBookPriority(id, high), id); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(`DELETE FROM metadata_source_pending WHERE book_id=? AND version=?`, id, versions[id]); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (m *metadataWorker) network(ctx context.Context) error {
	if m.results == nil {
		m.results = make(chan sourceResult, metadataSourceConcurrency)
		m.active = map[int64]bool{}
	}
	settings := m.w.settings
	if m.w.settingsPath != "" {
		settings = LoadSettings(m.w.settingsPath)
	}
	enabled := settingBool(settings, "auto_fill_meta", false)
	// Only this coordinator writes queue transitions; network goroutines return data.
	for len(m.results) > 0 {
		r := <-m.results
		delete(m.active, r.task.connection)
		if enabled && ctx.Err() == nil {
			if e := m.finishSource(r, settings); e != nil {
				return e
			}
		} else if e := m.coolSource(r.task.connection); e != nil {
			return e
		}
	}
	if !enabled || ctx.Err() != nil {
		return nil
	}
	conns, e := m.sourceConnections(settings)
	if e != nil {
		return e
	}
	rt := pluginruntime.NewRuntime(m.w.db, settings, m.w.registry)
	available := make([]*userdb.PluginConnection, 0, len(conns))
	for _, c := range conns {
		if m.sourceUnavailable(rt, c) == "" {
			available = append(available, c)
		}
	}
	if e = m.enrollSources(available); e != nil {
		return e
	}
	if len(available) == 0 {
		return nil
	}
	start := m.providerTurn % len(available)
	m.providerTurn++
	for i := 0; i < len(available) && len(m.active) < metadataSourceConcurrency; i++ {
		c := available[(start+i)%len(available)]
		if m.active[c.ID] {
			continue
		}
		var next int64
		var interval int
		if e = m.w.libraryDB.QueryRow(`SELECT next_at,interval_seconds FROM metadata_source_limits WHERE connection_id=?`, c.ID).Scan(&next, &interval); e != nil {
			return e
		}
		now := m.now().Unix()
		if next > now {
			continue
		}
		var task sourceTask
		task.connection = c.ID
		for priority := 1; priority >= 0; priority-- {
			e = m.w.libraryDB.QueryRow(`SELECT s.book_id,s.phase,s.record,s.attempts FROM metadata_source_jobs s JOIN metadata_enrichment_jobs j ON j.book_id=s.book_id WHERE s.connection_id=? AND s.priority=? AND s.phase<>'done' AND s.next_at<=? AND j.status IN ('network_pending','partial') ORDER BY s.next_at,s.book_id LIMIT 1`, c.ID, priority, now).Scan(&task.book, &task.phase, &task.record, &task.attempts)
			if e != sql.ErrNoRows {
				break
			}
		}
		if e == sql.ErrNoRows {
			continue
		}
		if e != nil {
			return e
		}
		var user int64
		e = m.w.db.QueryRow(`SELECT coalesce(collector_id,0) FROM items WHERE book_id=? ORDER BY collector_id LIMIT 1`, task.book).Scan(&user)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if user == 0 {
			if e = m.saveJob(task.book, "needs_review", "书籍没有可用的所属用户", 0); e != nil {
				return e
			}
			continue
		}
		if c.OwnerType == "user" && c.OwnerID != user {
			if _, e = m.w.libraryDB.Exec(`UPDATE metadata_source_jobs SET phase='done',last_error='来源不属于此书用户' WHERE book_id=? AND connection_id=?`, task.book, c.ID); e != nil {
				return e
			}
			continue
		}
		books, e := library.GetBooksData(m.w.libraryDB, []int64{task.book})
		if e != nil {
			return e
		}
		if len(books) == 0 {
			if e = m.saveJob(task.book, "deleted", "书籍已删除", 0); e != nil {
				return e
			}
			continue
		}
		book := books[0]
		if localmeta.ISBN(book.ISBN) == "" && localmeta.MissingAuthors(book.Authors) {
			if e = m.saveJob(task.book, "needs_review", "文件中缺少可验证的 ISBN 或作者，不自动套用同名书", 0); e != nil {
				return e
			}
			continue
		}
		method := "search_books"
		args := []any{referenceQuery(&book)}
		var record map[string]any
		_ = json.Unmarshal([]byte(task.record), &record)
		if task.phase == "detail" {
			method = "get_metadata"
			args = []any{stringOf(record["provider_value"])}
		}
		if task.phase == "cover" {
			if settingBool(settings, "auto_fill_keep_cover", true) || book.HasCover {
				if e = m.setSourcePhase(task, "done", record, 1, ""); e != nil {
					return e
				}
				if e = m.rollupSourceBook(task.book, settings); e != nil {
					return e
				}
				continue
			}
			method = "get_cover"
			args = []any{stringOf(record["cover_url"])}
		}
		bounded := *c
		bounded.Config = map[string]any{}
		for k, v := range c.Config {
			bounded.Config[k] = v
		}
		bounded.Config["timeout_seconds"] = 10.0
		bounded.Config["max_retries"] = 0.0
		if _, e = m.w.libraryDB.Exec(`UPDATE metadata_source_limits SET next_at=?,last_request_at=?,requests=requests+1 WHERE connection_id=?`, now+int64(max(1, interval)), now, c.ID); e != nil {
			return e
		}
		m.active[c.ID] = true
		go func(task sourceTask, connection userdb.PluginConnection, user int64, method string, args []any) {
			timeout := 10.0
			value, err := rt.Read(&connection, method, args, pluginruntime.CallOptions{Timeout: &timeout, RequestedBy: &user, RequiredScopes: []string{"books.read"}, AuditData: map[string]any{"book_id": task.book, "method": method, "background_metadata": true}})
			m.results <- sourceResult{task: task, value: value, err: err}
		}(task, bounded, user, method, args)
	}
	return nil
}

func boundedSourceRecord(record map[string]any) (map[string]any, bool) {
	if record == nil {
		return nil, true
	}
	filtered := map[string]any{}
	for _, key := range []string{"title", "authors", "publisher", "isbn", "comments", "language", "pubdate", "tags", "cover_url", "provider_value"} {
		if v, ok := record[key]; ok {
			filtered[key] = v
		}
	}
	data, e := json.Marshal(filtered)
	return filtered, e == nil && len(data) <= 256<<10
}

func (m *metadataWorker) setSourcePhase(t sourceTask, phase string, record map[string]any, matched int, reason string) error {
	if phase == "done" {
		record = nil
	}
	if record != nil {
		filtered := map[string]any{}
		for _, key := range []string{"title", "authors", "publisher", "isbn", "comments", "language", "pubdate", "tags", "cover_url", "provider_value"} {
			if v, ok := record[key]; ok {
				filtered[key] = v
			}
		}
		record = filtered
	}
	data, e := json.Marshal(record)
	if len(data) > 256<<10 {
		phase = "done"
		matched = 0
		reason = "来源元数据超过缓存上限"
		data = []byte("null")
	}
	if e != nil {
		return e
	}
	_, e = m.w.libraryDB.Exec(`UPDATE metadata_source_jobs SET phase=?,record=?,matched=max(matched,?),next_at=0,last_error=? WHERE book_id=? AND connection_id=?`, phase, string(data), matched, reason, t.book, t.connection)
	return e
}

func (m *metadataWorker) finishSource(r sourceResult, settings map[string]any) error {
	t := r.task
	if e := m.coolSource(t.connection); e != nil {
		return e
	}
	db := m.w.libraryDB
	var phase string
	if e := db.QueryRow(`SELECT phase FROM metadata_source_jobs WHERE book_id=? AND connection_id=?`, t.book, t.connection).Scan(&phase); e != nil {
		return e
	}
	if phase != t.phase {
		return nil
	}
	var bookStatus string
	if e := db.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=?`, t.book).Scan(&bookStatus); e != nil {
		return e
	}
	if bookStatus != "network_pending" && bookStatus != "partial" {
		return m.setSourcePhase(t, "done", nil, 0, "")
	}
	// A connection may be disabled or change ownership while a request is in flight.
	var user int64
	if e := m.w.db.QueryRow(`SELECT coalesce(collector_id,0) FROM items WHERE book_id=? ORDER BY collector_id LIMIT 1`, t.book).Scan(&user); e != nil && e != sql.ErrNoRows {
		return e
	}
	rt := pluginruntime.NewRuntime(m.w.db, settings, m.w.registry)
	conns, e := rt.ConnectionsFor(metadataLookupCapability, &user)
	if e != nil {
		return e
	}
	var conn *userdb.PluginConnection
	for _, c := range conns {
		if c.ID == t.connection {
			conn = c
			break
		}
	}
	if conn == nil || user == 0 {
		return nil
	}
	if r.err != nil {
		// Errors never include upstream URLs, tokens, or exception text in public status.
		var failures int
		if e = db.QueryRow(`SELECT failures FROM metadata_source_limits WHERE connection_id=?`, t.connection).Scan(&failures); e != nil {
			return e
		}
		delay := min(600, 30*(1<<min(failures, 5)))
		code := "request_failed"
		var upstream *pluginproto.UpstreamError
		if errors.As(r.err, &upstream) {
			if upstream.Code == "provider_rate_limited" {
				code = "provider_rate_limited"
				if upstream.RetryAfter == nil {
					delay = max(delay, 300)
				}
				if upstream.RetryAfter != nil {
					delay = max(delay, int(math.Min(604800, math.Ceil(*upstream.RetryAfter))))
				}
			}
			if upstream.Code == "provider_unauthorized" {
				code = "provider_unauthorized"
				delay = max(delay, 3600)
			}
		}
		terminal := t.attempts+1 >= 3
		target := t.phase
		if terminal {
			target = "done"
		}
		if _, e = db.Exec(`UPDATE metadata_source_limits SET failures=failures+1,next_at=max(next_at,?),last_error=? WHERE connection_id=?`, sourceClock(m.now())+int64(delay), code, t.connection); e != nil {
			return e
		}
		if _, e = db.Exec(`UPDATE metadata_source_jobs SET phase=?,attempts=attempts+1,next_at=?,last_error='来源请求失败' WHERE book_id=? AND connection_id=?`, target, sourceClock(m.now())+int64(delay), t.book, t.connection); e != nil {
			return e
		}
		if terminal {
			return m.rollupSourceBook(t.book, settings)
		}
		return nil
	}
	if _, e = db.Exec(`UPDATE metadata_source_limits SET failures=0,last_error='' WHERE connection_id=?`, t.connection); e != nil {
		return e
	}
	var record map[string]any
	_ = json.Unmarshal([]byte(t.record), &record)
	identity, e := library.MetadataIdentity(db, t.book)
	if e != nil {
		return e
	}
	books, e := library.GetBooksData(db, []int64{t.book})
	if e != nil {
		return e
	}
	if len(books) == 0 {
		return nil
	}
	book := books[0]
	if t.phase == "search" {
		record = matchingMetadata(book, pluginRecords(pluginBooksOf(r.value)))
	}
	if t.phase == "detail" {
		if candidate := pluginRecordDict(r.value); matchingMetadata(book, []map[string]any{candidate}) != nil {
			for key, value := range candidate {
				if value != nil && stringOf(value) != "" {
					record[key] = value
				}
			}
		}
	}
	if record == nil || matchingMetadata(book, []map[string]any{record}) == nil {
		if e = m.setSourcePhase(t, "done", nil, 0, "未找到可唯一确认的版本"); e != nil {
			return e
		}
		return m.rollupSourceBook(t.book, settings)
	}
	var safe bool
	record, safe = boundedSourceRecord(record)
	if !safe {
		if e = m.setSourcePhase(t, "done", nil, 0, "来源元数据超过缓存上限"); e != nil {
			return e
		}
		return m.rollupSourceBook(t.book, settings)
	}
	if t.phase == "cover" {
		if !settingBool(settings, "auto_fill_keep_cover", true) && !book.HasCover && m.w.libraryRoot != "" {
			if image, ok := r.value.(*pluginiface.CoverResult); ok && image != nil && len(image.Content) > 0 {
				if _, e = library.StoreCover(db, m.w.libraryRoot, t.book, image.Content); e != nil {
					return e
				}
			}
		}
		if e = m.setSourcePhase(t, "done", record, 1, ""); e != nil {
			return e
		}
		return m.rollupSourceBook(t.book, settings)
	}
	key, e := rt.PluginKeyOf(conn)
	if e != nil {
		return e
	}
	changed, e := library.FillMissingMetadataGuarded(db, t.book, metadataCandidate(record), identity, "network:"+key)
	if e != nil {
		return e
	}
	if len(changed) > 0 {
		m.logger.Printf("book=%d 来源=%s 补全: %v", t.book, key, changed)
	}
	next := "done"
	if t.phase == "search" && stringOf(record["provider_value"]) != "" {
		next = "detail"
	} else if !settingBool(settings, "auto_fill_keep_cover", true) && !book.HasCover && stringOf(record["cover_url"]) != "" {
		next = "cover"
	}
	if e = m.setSourcePhase(t, next, record, 1, ""); e != nil {
		return e
	}
	return m.rollupSourceBook(t.book, settings)
}

func (m *metadataWorker) rollupSourceBook(id int64, settings map[string]any) error {
	var status string
	if e := m.w.libraryDB.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=?`, id).Scan(&status); e != nil {
		return e
	}
	if status == "protected" {
		return nil
	}
	var user int64
	if e := m.w.db.QueryRow(`SELECT coalesce(collector_id,0) FROM items WHERE book_id=? ORDER BY collector_id LIMIT 1`, id).Scan(&user); e != nil {
		return e
	}
	rt := pluginruntime.NewRuntime(m.w.db, settings, m.w.registry)
	conns, e := rt.ConnectionsFor(metadataLookupCapability, &user)
	if e != nil {
		return e
	}
	matched := 0
	relevant := 0
	for _, c := range conns {
		if m.sourceUnavailable(rt, c) != "" {
			continue
		}
		relevant++
		var phase string
		var hasMatch int
		e = m.w.libraryDB.QueryRow(`SELECT phase,matched FROM metadata_source_jobs WHERE book_id=? AND connection_id=?`, id, c.ID).Scan(&phase, &hasMatch)
		if e == sql.ErrNoRows || phase != "done" {
			return nil
		}
		if e != nil {
			return e
		}
		matched += hasMatch
	}
	if relevant == 0 {
		return nil
	}
	if matched == 0 {
		return m.saveJob(id, "needs_review", "所有可用来源均已处理，未取得可唯一确认的信息", 0)
	}
	books, e := library.GetBooksData(m.w.libraryDB, []int64{id})
	if e != nil {
		return e
	}
	if len(books) == 0 {
		return nil
	}
	b := books[0]
	if localmeta.ISBN(b.ISBN) != "" && b.Publisher != "" && !localmeta.MissingAuthors(b.Authors) && b.Comments != "" {
		return m.saveJob(id, "complete", "", 0)
	}
	return m.saveJob(id, "partial", "所有可用来源均已处理，仍缺少部分信息", 0)
}

func sourceBookPriority(id, initialMax int64) int {
	if id > initialMax {
		return 1
	}
	return 0
}

// Completion-based cooldown also covers discarded results and graceful restarts.
func (m *metadataWorker) coolSource(connection int64) error {
	now := sourceClock(m.now())
	_, e := m.w.libraryDB.Exec(`UPDATE metadata_source_limits SET last_request_at=max(last_request_at,?),next_at=max(next_at,?+interval_seconds) WHERE connection_id=?`, now, now, connection)
	return e
}
func sourceClock(t time.Time) int64 {
	n := t.Unix()
	if t.Nanosecond() > 0 {
		n++
	}
	return n
}

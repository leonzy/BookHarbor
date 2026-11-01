package httpapi

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/scan"
	"talebook/booksvr/internal/sourcefile"
)

const lectureCursor = "lecture_recheck_v1_cursor"
const lectureMaximum = "lecture_recheck_v1_max"
const lectureDiscoveryDone = "lecture_recheck_v1_discovered"

func lectureSourceName(path string) (string, error) {
	if sourcefile.IsReference(path) {
		ref, err := sourcefile.Decode(path)
		if err != nil {
			return "", err
		}
		path = ref.Name
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), nil
}

// One bounded traversal of indexed format rows; never walk source directories.
func (m *metadataWorker) discoverLectureRechecks() error {
	done, err := m.state(lectureDiscoveryDone)
	if err != nil || done != 0 {
		return err
	}
	db := m.w.libraryDB
	var high int64
	err = db.QueryRow(`SELECT value FROM metadata_enrichment_state WHERE key=?`, lectureMaximum).Scan(&high)
	if err == sql.ErrNoRows {
		if err = db.QueryRow(`SELECT coalesce(max(id),0) FROM book_formats`).Scan(&high); err != nil {
			return err
		}
		if err = m.setState(lectureMaximum, high); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	cursor, err := m.state(lectureCursor)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id,book_id,format,path FROM book_formats WHERE id>? AND id<=? ORDER BY id LIMIT 5000`, cursor, high)
	if err != nil {
		return err
	}
	type candidate struct {
		id   int64
		name string
	}
	var candidates []candidate
	last := cursor
	for rows.Next() {
		var fid, bid int64
		var format, path string
		if err = rows.Scan(&fid, &bid, &format, &path); err != nil {
			rows.Close()
			return err
		}
		last = fid
		if !strings.EqualFold(format, "TXT") {
			continue
		}
		name, e := lectureSourceName(path)
		if e != nil {
			continue
		}
		if title, _ := localmeta.DatedLectureFilename(name); title != "" {
			candidates = append(candidates, candidate{bid, name})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range candidates {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO metadata_lecture_rechecks(book_id,source_name) VALUES(?,?)`, c.id, c.name); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, lectureCursor, last); err != nil {
		return err
	}
	if last == cursor || last >= high {
		if _, err = tx.Exec(`INSERT INTO metadata_enrichment_state(key,value) VALUES(?,1) ON CONFLICT(key) DO UPDATE SET value=1`, lectureDiscoveryDone); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *metadataWorker) lectureResult(id int64, status, reason string, delay time.Duration) error {
	_, err := m.w.libraryDB.Exec(`UPDATE metadata_lecture_rechecks SET status=?,reason=?,next_at=?,updated_at=? WHERE book_id=?`, status, reason, m.now().Add(delay).Unix(), m.now().Unix(), id)
	return err
}

func sameLecturePeople(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string{}, a...)
	b = append([]string{}, b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// A pass processes at most one candidate per normal tick under the existing lease.
func (m *metadataWorker) recheckLectureOne(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db := m.w.libraryDB
	var id, created int64
	var name string
	err := db.QueryRow(`SELECT book_id,source_name,created_at FROM metadata_lecture_rechecks WHERE status IN ('pending','unreadable') AND next_at<=? ORDER BY next_at,book_id LIMIT 1`, m.now().Unix()).Scan(&id, &name, &created)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var existing string
	err = db.QueryRow(`SELECT status FROM metadata_enrichment_jobs WHERE book_id=?`, id).Scan(&existing)
	if err == sql.ErrNoRows {
		if err = library.ProtectHistoricalMetadataEdits(db, id); err == sql.ErrNoRows {
			return m.lectureResult(id, "deleted", "书籍已删除", 0)
		} else if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var locked int
	if err = db.QueryRow(`SELECT count(*) FROM metadata_field_locks WHERE book_id=? AND field IN ('title','authors','author_sort')`, id).Scan(&locked); err != nil {
		return err
	}
	if locked > 0 || existing == "protected" {
		return m.lectureResult(id, "protected", "保留人工书名、作者或排序字段", 0)
	}
	books, err := library.GetBooksData(db, []int64{id})
	if err != nil {
		return err
	}
	if len(books) == 0 {
		return m.lectureResult(id, "deleted", "书籍已删除", 0)
	}
	book := books[0]
	expected, err := library.MetadataIdentity(db, id)
	if err != nil {
		return err
	}
	path, missing, err := library.ResolvePath(db, id, "TXT")
	if err != nil || missing || path == "" {
		return m.lectureResult(id, "unreadable", "来源暂不可读，保留书籍并延后重试", time.Hour)
	}
	current, e := lectureSourceName(path)
	if e != nil || current != name {
		return m.lectureResult(id, "conflict", "来源文件名已变化，未套用旧候选", 0)
	}
	if !sourcefile.IsReference(path) && !filepath.IsAbs(path) {
		path = filepath.Join(m.w.libraryRoot, path)
	}
	if sourcefile.IsReference(path) {
		ref, e := sourcefile.Decode(path)
		if e != nil || ref.Size > 32<<20 {
			return m.lectureResult(id, "unreadable", "来源超出本次读取预算，未修改书籍", time.Hour)
		}
	}
	info, err := sourcefile.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return m.lectureResult(id, "unreadable", "来源暂不可读，保留书籍并延后重试", time.Hour)
	}
	run, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	meta, err := scan.ReadMetadata(run, path, "TXT")
	if e := ctx.Err(); e != nil {
		return e
	}
	if err != nil {
		return m.lectureResult(id, "unreadable", "来源读取暂未成功，稍后重试", time.Hour)
	}
	title, author := localmeta.DatedLectureFilename(name)
	if meta.Title != title || !localmeta.LectureAuthorsMatch(meta.Authors, author) {
		return m.lectureResult(id, "no_evidence", "正文未能同时确认标题与主讲人", 0)
	}
	currentIdentity, err := library.MetadataIdentity(db, id)
	if err != nil {
		return err
	}
	for field, value := range expected {
		if currentIdentity[field] != value {
			return m.lectureResult(id, "conflict", "核查期间信息发生变化，保留当前信息", 0)
		}
	}
	if book.Title != strings.TrimSpace(name) && book.Title != title {
		return m.lectureResult(id, "conflict", "现有书名非原文件名，保留已有信息", 0)
	}
	if !localmeta.MissingAuthors(book.Authors) && !sameLecturePeople(book.Authors, meta.Authors) {
		return m.lectureResult(id, "conflict", "已有不同作者，未覆盖现有信息", 0)
	}
	if book.Title == title && sameLecturePeople(book.Authors, meta.Authors) {
		var repaired bool
		if err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM metadata_enrichment_writes WHERE book_id=? AND origin='file' AND field IN ('title','authors') AND created_at>=?)`, id, created).Scan(&repaired); err != nil {
			return err
		}
		if repaired {
			return m.lectureResult(id, "repaired", "文件来源修正已完成", 0)
		}
		return m.lectureResult(id, "correct", "正文核验通过，原信息已正确", 0)
	}
	if err = m.applyLocalMetadata(id, meta, existing, expected); err != nil {
		return err
	}
	books, err = library.GetBooksData(db, []int64{id})
	if err != nil {
		return err
	}
	if len(books) == 0 {
		return m.lectureResult(id, "deleted", "书籍已删除", 0)
	}
	if books[0].Title == title && sameLecturePeople(books[0].Authors, meta.Authors) {
		return m.lectureResult(id, "repaired", "已按正文佐证修正书名与主讲人", 0)
	}
	return m.lectureResult(id, "conflict", "核查期间信息变化或受保护，未覆盖现有信息", 0)
}

func lectureRecheckStatus(db *sql.DB) (map[string]any, error) {
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='metadata_lecture_rechecks')`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := db.Query(`SELECT status,count(*) FROM metadata_lecture_rechecks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	var total int64
	for rows.Next() {
		var status string
		var n int64
		if err = rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		counts[status] = n
		total += n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var done int64
	if err = db.QueryRow(`SELECT coalesce((SELECT value FROM metadata_enrichment_state WHERE key=?),0)`, lectureDiscoveryDone).Scan(&done); err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "checked": total - counts["pending"], "discovery_done": done != 0, "counts": counts}, nil
}

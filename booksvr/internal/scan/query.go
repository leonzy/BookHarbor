package scan

import (
	"database/sql"
	"fmt"
	"strings"
	"talebook/booksvr/internal/importdir"

	"talebook/booksvr/internal/userdb"
)

// Summary separates cumulative processed files from current library books.
type Summary struct {
	Total              int64 `json:"total"`
	Done               int64 `json:"done"`
	Todo               int64 `json:"todo"`
	Failed             int64 `json:"failed"`
	Skipped            int64 `json:"skipped"`
	LibraryBooks       int64 `json:"library_books"`
	AvailableBooks     int64 `json:"available_books"`
	MissingSourceBooks int64 `json:"missing_source_books"`
}

// Successful files exclude duplicates, which belong to skipped history.
var doneStatuses = []string{
	userdb.ScanStatusImported,
	userdb.ScanStatusIndexed,
}

// failedStatuses 与 Python 的 failed_status 一致。
var failedStatuses = []string{
	userdb.ScanStatusFailed,
	userdb.ScanStatusDeleteFailed,
}

// Dropped records have already been skipped and need no import action.
var skippedStatuses = []string{userdb.ScanStatusDrop, userdb.ScanStatusExist}

// countStatuses 复刻 Scanner.count() 的初始键集合与顺序。
//
// 注意：Python 用 dict 初始化，键顺序即插入顺序；Go 用 map 无法保序，
// 因此调用方若需要稳定输出应自行排序。这里保留全部键，未知状态动态追加。
var countStatuses = []string{
	userdb.ScanStatusNew,
	userdb.ScanStatusDrop,
	userdb.ScanStatusExist,
	userdb.ScanStatusReady,
	userdb.ScanStatusQueued,
	userdb.ScanStatusImporting,
	userdb.ScanStatusImported,
	userdb.ScanStatusIndexed,
	userdb.ScanStatusDeleteFailed,
	userdb.ScanStatusFailed,
}

// Summary 复刻 Scanner.summary()。
func (s *Service) Summary() (Summary, error) {
	var out Summary
	rows, err := s.DB.Query(`SELECT status,COUNT(*) FROM scanfiles WHERE status != 'expanded' GROUP BY status`)
	if err != nil {
		return out, fmt.Errorf("scan: 统计文件失败: %w", err)
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.Total += n
		switch st {
		case userdb.ScanStatusImported, userdb.ScanStatusIndexed:
			out.Done += n
		case userdb.ScanStatusFailed, userdb.ScanStatusDeleteFailed:
			out.Failed += n
		case userdb.ScanStatusExist, userdb.ScanStatusDrop:
			out.Skipped += n
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Todo = out.Total - out.Done - out.Failed - out.Skipped
	if s.LibraryDB != nil {
		tx, err := s.LibraryDB.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err := tx.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&out.LibraryBooks); err != nil {
			return out, err
		}
		// Older/minimal libraries may not yet have the derived availability table.
		var has int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='book_unavailable_sources'`).Scan(&has); err != nil {
			return out, err
		}
		if has > 0 {
			if err := tx.QueryRow(`SELECT COUNT(*) FROM book_unavailable_sources u JOIN books b ON b.id=u.book_id`).Scan(&out.MissingSourceBooks); err != nil {
				return out, err
			}
		}
		out.AvailableBooks = out.LibraryBooks - out.MissingSourceBooks
	}
	return out, nil
}

// EnsureQueryIndexes runs at worker startup, never during status polling.
func EnsureQueryIndexes(db *sql.DB) error {
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS scanfiles_scan_status_idx ON scanfiles(scan_id,status)`,
		`CREATE INDEX IF NOT EXISTS scanfiles_import_status_idx ON scanfiles(import_id,status)`,
		`CREATE INDEX IF NOT EXISTS scanfiles_created_idx ON scanfiles(create_time)`,
		`CREATE INDEX IF NOT EXISTS scanfiles_status_created_idx ON scanfiles(status,create_time,id)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// CountByStatus 复刻 Scanner.count(query)：按状态计数，未知状态动态追加。
func (s *Service) CountByStatus(scanID, importID int64, byImport bool) (map[string]int64, error) {
	count := make(map[string]int64, len(countStatuses)+1)
	count["total"] = 0
	for _, st := range countStatuses {
		count[st] = 0
	}

	col := "scan_id"
	if byImport {
		col = "import_id"
	}
	rows, err := s.DB.Query(`SELECT status,COUNT(*) FROM scanfiles WHERE status != 'expanded' AND `+col+` = ? GROUP BY status`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scan: 查询状态失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("scan: 读取状态失败: %w", err)
		}
		count[st] += n
		count["total"] += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan: 遍历状态失败: %w", err)
	}
	return count, nil
}

// ScanStatus 复刻 Scanner.scan_status()，返回 (scan_id, 计数)。
func (s *Service) ScanStatus() (int64, map[string]int64, error) {
	return s.statusBy("scan_id")
}

// ImportStatus 复刻 Scanner.import_status()，返回 (import_id, 计数)。
func (s *Service) ImportStatus() (int64, map[string]int64, error) {
	return s.statusBy("import_id")
}

func (s *Service) statusBy(col string) (int64, map[string]int64, error) {
	var id sql.NullInt64
	if err := s.DB.QueryRow(`SELECT MAX(` + col + `) FROM scanfiles`).Scan(&id); err != nil {
		return 0, nil, fmt.Errorf("scan: 查询最大 %s 失败: %w", col, err)
	}
	if !id.Valid {
		count := map[string]int64{"total": 0}
		for _, st := range countStatuses {
			count[st] = 0
		}
		return 0, count, nil
	}
	count, err := s.CountByStatus(id.Int64, 0, col == "import_id")
	if err != nil {
		return 0, nil, err
	}
	return id.Int64, count, nil
}

// CountReady 复刻 Scanner.build_query(hashlist).count()。
//
// hashlist 为 nil 表示统计全部 READY 记录。
func (s *Service) CountReady(hashlist []string) (int64, error) {
	q := `SELECT COUNT(*) FROM scanfiles WHERE status = ?`
	args := []any{userdb.ScanStatusReady}
	if hashlist != nil {
		if len(hashlist) == 0 {
			return 0, nil
		}
		q += ` AND hash IN (` + placeholders(len(hashlist)) + `)`
		args = append(args, toArgs(hashlist)...)
	}
	var n int64
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("scan: 统计待导入失败: %w", err)
	}
	return n, nil
}

// Delete 复刻 Scanner.delete(hashlist)。
//
// hashlist 为 nil 表示删除全部；否则按 hash 列表或单个 hash 删除。
func (s *Service) Delete(hashlist []string) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if hashlist == nil {
		res, err = s.DB.Exec(`DELETE FROM scanfiles`)
	} else if len(hashlist) == 0 {
		return 0, nil
	} else {
		q := `DELETE FROM scanfiles WHERE hash IN (` + placeholders(len(hashlist)) + `)`
		res, err = s.DB.Exec(q, toArgs(hashlist)...)
	}
	if err != nil {
		return 0, fmt.Errorf("scan: 删除失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("scan: 读取删除行数失败: %w", err)
	}
	return n, nil
}

// ListFilter 复刻 ScanList 的 filter 参数。
type ListFilter string

const (
	FilterAll     ListFilter = "all"
	FilterTodo    ListFilter = "todo"
	FilterDone    ListFilter = "done"
	FilterFailed  ListFilter = "failed"
	FilterSkipped ListFilter = "skipped"
)

// ListOptions 复刻 ScanList 的查询参数。
type ListOptions struct {
	Num    int    // <= 0 表示不分页
	Page   int    // 从 0 开始
	Sort   string // id/path/name/create_time/update_time
	Desc   bool
	Filter ListFilter
}

// ReadyBatch returns a stable cursor page, allowing automatic imports to process
// arbitrarily many books without exceeding SQLite's placeholder limit.
func (s *Service) ReadyBatch(afterID int64, limit int) ([]*userdb.ScanFile, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT `+scanFileColumns+` FROM scanfiles WHERE status = ? AND id > ? ORDER BY id LIMIT ?`, userdb.ScanStatusReady, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*userdb.ScanFile{}
	for rows.Next() {
		row, err := scanRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// RecoveredReadyBatch gives verified compatibility retries the next boundary
// in the existing worker, without advancing or replacing its ordinary cursor.
func (s *Service) RecoveredReadyBatch(roots []string, limit int) ([]*userdb.ScanFile, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT ` + scanFileColumns + ` FROM scanfiles WHERE status='ready' AND data LIKE '%"previous_analysis_error"%' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*userdb.ScanFile
	for rows.Next() {
		row, err := scanRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		if _, verified := row.Data["previous_analysis_error"]; !verified {
			continue
		}
		for _, root := range roots {
			if importdir.IsWithin(row.Path, root) {
				result = append(result, row)
				break
			}
		}
		if len(result) >= limit {
			break
		}
	}
	return result, rows.Err()
}

// sortColumns 复刻 ScanList 的 order 映射。
var sortColumns = map[string]string{
	"id":          "id",
	"path":        "path",
	"name":        "name",
	"create_time": "create_time",
	"update_time": "update_time",
}

// List 复刻 ScanList.get() 的查询部分，返回 (条目, 总数)。
func (s *Service) List(opts ListOptions) ([]*userdb.ScanFile, int64, error) {
	col, ok := sortColumns[opts.Sort]
	if !ok {
		col = "create_time"
	}
	order := "DESC"
	if !opts.Desc {
		order = "ASC"
	}

	where := " WHERE status != 'expanded'"
	var args []any
	switch opts.Filter {
	case FilterTodo:
		all := append(append([]string{}, doneStatuses...), failedStatuses...)
		all = append(all, skippedStatuses...)
		where += ` AND status NOT IN (` + placeholders(len(all)) + `)`
		args = toArgs(all)
	case FilterDone:
		where += ` AND status IN (` + placeholders(len(doneStatuses)) + `)`
		args = toArgs(doneStatuses)
	case FilterFailed:
		where += ` AND status IN (` + placeholders(len(failedStatuses)) + `)`
		args = toArgs(failedStatuses)
	case FilterSkipped:
		where += ` AND status IN (` + placeholders(len(skippedStatuses)) + `)`
		args = toArgs(skippedStatuses)
	}

	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM scanfiles`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("scan: 统计列表失败: %w", err)
	}
	// An empty queue needs no sorted row query over the wide metadata table.
	if total == 0 {
		return []*userdb.ScanFile{}, 0, nil
	}

	columns := `id, scan_id, import_id, name, path, hash, status, title, author,
		publisher, tags, create_time, update_time, book_id, data
		`
	q := `SELECT ` + columns + ` FROM scanfiles` + where + ` ORDER BY ` + col + ` ` + order
	qargs := append([]any{}, args...)
	if opts.Num > 0 {
		// Select page keys from a covering index before fetching any wide JSON
		// rows. Otherwise SQLite can choose status/id and sort every full row.
		hint := map[string]string{"create_time": "scanfiles_status_created_idx", "id": "scanfiles_status_id_idx", "path": "idx_source_scan_path"}[col]
		if hint != "" {
			var exists int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, hint).Scan(&exists); err != nil {
				return nil, 0, err
			}
			if exists > 0 {
				q = pagedListQuery(columns, where, col, order, hint)
			} else {
				q += ` LIMIT ? OFFSET ?`
			}
		} else {
			q += ` LIMIT ? OFFSET ?`
		}
		qargs = append(qargs, opts.Num, opts.Page*opts.Num)
	}

	rows, err := s.DB.Query(q, qargs...)
	if err != nil {
		return nil, 0, fmt.Errorf("scan: 查询列表失败: %w", err)
	}
	defer rows.Close()

	var out []*userdb.ScanFile
	for rows.Next() {
		row, err := scanRow(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan: 遍历列表失败: %w", err)
	}
	return out, total, nil
}

func pagedListQuery(columns, where, col, order, index string) string {
	ordering := col + " " + order + ", id " + order
	return `SELECT ` + columns + ` FROM scanfiles WHERE id IN (SELECT id FROM scanfiles INDEXED BY ` + index + where + ` ORDER BY ` + ordering + ` LIMIT ? OFFSET ?) ORDER BY ` + ordering
}

// placeholders 生成 "?,?,?" 形式的占位符串。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// toArgs 把字符串切片转成 []any。
func toArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

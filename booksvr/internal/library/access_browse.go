package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"talebook/booksvr/internal/access"
)

// AccessBrowseQuery is deliberately separate from the legacy fast path until
// every public reader has been wired. Directory is a root-relative validated
// selector, not a rebuildable directory-node rowid.
type AccessBrowseQuery struct {
	BrowseQuery
	Directory         *access.Target
	Search            string
	SearchRelevance   bool
	SearchAliasIDs    []int64
	SearchAuthors     []string
	PhysicalDirectory string
	CategoryID        int64
	CategoryKind      string
}

func accessFilePredicate(scope AccessScope, q AccessBrowseQuery) (string, []any, error) {
	if !scope.valid || scope.profile == "" {
		return "", nil, ErrAccessNotReady
	}
	parts := []string{"f.revision=?", "f.profile=?", "f.owner IN ('',?)"}
	args := []any{scope.revision, scope.profile, scope.principal}
	if q.PhysicalDirectory != "" {
		if q.DirectoryRecursive {
			prefix := strings.TrimSuffix(q.PhysicalDirectory, "/") + "/"
			parts = append(parts, "(f.physical_directory=? OR (f.physical_directory>=? AND f.physical_directory<?))")
			args = append(args, q.PhysicalDirectory, prefix, strings.TrimSuffix(prefix, "/")+"0")
		} else {
			parts = append(parts, "f.physical_directory=?")
			args = append(args, q.PhysicalDirectory)
		}
	}
	if q.FormatFilter || (q.Format != "" && q.Format != "全部") {
		parts = append(parts, "f.format=UPPER(?)")
		args = append(args, q.Format)
	}
	if d := q.Directory; d != nil {
		if d.Root == "" || d.Directory == "" || d.Class != "" {
			return "", nil, fmt.Errorf("invalid directory selector")
		}
		parts = append(parts, "f.source_root=?")
		args = append(args, d.Root)
		if d.Recursive {
			prefix := strings.TrimSuffix(d.Directory, "/") + "/"
			// Binary prefix bounds retain the composite directory index and
			// distinguish /child from /children without wildcard interpretation.
			parts = append(parts, "(f.directory=? OR (f.directory>=? AND f.directory<?))")
			args = append(args, d.Directory, prefix, strings.TrimSuffix(prefix, "/")+"0")
		} else {
			parts = append(parts, "f.directory=?")
			args = append(args, d.Directory)
		}
	}
	return strings.Join(parts, " AND "), args, nil
}

// AccessBrowseBooks applies permission before filters, work collapsing, count
// and pagination. It never expands permitted IDs into a Go slice.
func AccessBrowseBooks(ctx context.Context, db *sql.DB, scope AccessScope, q AccessBrowseQuery) ([]int64, int, error) {
	if accessSimpleGrouped(q) {
		return accessGroupedPage(ctx, db, scope, q)
	}
	fileWhere, fileArgs, err := accessFilePredicate(scope, q)
	if err != nil {
		return nil, 0, err
	}
	base := q.BrowseQuery
	base.DirectoryNode = 0
	base.Format = ""
	base.FormatFilter = false
	where, args, err := base.where(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(q.Search) != "" {
		search, searchArgs, e := accessSearchPredicate(scope, q.Search)
		if e != nil {
			return nil, 0, e
		}
		if len(q.SearchAliasIDs) > 0 {
			search = "(" + search + ") OR b.id IN (SELECT value FROM json_each(?))"
			searchArgs = append(searchArgs, jsonIDs(q.SearchAliasIDs))
		}
		if len(q.SearchAuthors) > 0 {
			encoded, e := json.Marshal(q.SearchAuthors)
			if e != nil {
				return nil, 0, e
			}
			search = "(" + search + ") OR b.id IN (SELECT l.book FROM authors a JOIN books_authors_link l ON l.author=a.id WHERE a.name IN (SELECT value FROM json_each(?)))"
			searchArgs = append(searchArgs, string(encoded))
		}
		where += " AND (" + search + ")"
		args = append(args, searchArgs...)
	}
	if q.CategoryID != 0 {
		spec, ok := categorySpecs[q.CategoryKind]
		if !ok {
			return nil, 0, fmt.Errorf("invalid category selector")
		}
		where += fmt.Sprintf(" AND b.id IN (SELECT book FROM %s WHERE %s=?)", spec.linkTable, spec.linkItemCol)
		args = append(args, q.CategoryID)
	}
	filterWhere, filterArgs := where, append([]any{}, args...)
	where += " AND b.id IN (SELECT f.book_id FROM acl_profile_file f WHERE " + fileWhere + ")"
	args = append(args, fileArgs...)
	baseWhere, baseArgs := where, append([]any{}, args...)
	grouped := false
	if q.GroupWorks {
		hidden, e := identityHiddenSQL(ctx, db, where)
		if e != nil {
			return nil, 0, e
		}
		if hidden != "" {
			grouped = true
			where += " AND b.id NOT IN (" + hidden + ")"
			args = append(args, baseArgs...)
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int
	fileFiltered := q.Directory != nil || q.PhysicalDirectory != "" || q.FormatFilter || (q.Format != "" && q.Format != "全部")
	simple := !q.GroupWorks && !fileFiltered && q.Search == "" && q.Candidates == nil && q.Category == "" && q.CategoryID == 0 && len(q.Excluded) == 0
	for _, v := range q.Filters {
		if v != "" && v != "全部" {
			simple = false
		}
	}
	countSQL, countArgs := `SELECT count(*) FROM books b WHERE `+where, args
	if simple {
		countSQL = `SELECT coalesce(sum(books),0) FROM acl_profile_count WHERE revision=? AND profile=? AND owner IN ('',?)`
		countArgs = []any{scope.revision, scope.profile, scope.principal}
	}
	if err = tx.QueryRowContext(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "b.id DESC"
	if q.TitleSort {
		order = "p.title,b.id DESC"
	} else if q.TimestampSort {
		order = "p.timestamp DESC,b.id DESC"
	}
	from := `acl_profile_book p CROSS JOIN books b ON b.id=p.book_id`
	pageArgs := []any{}
	if q.SizeSort != "" {
		size := "p.size"
		if fileFiltered {
			from += ` JOIN (SELECT f.book_id,MAX(f.size) size FROM acl_profile_file f WHERE ` + fileWhere + ` GROUP BY f.book_id) sizes ON sizes.book_id=b.id`
			pageArgs = append(pageArgs, fileArgs...)
			size = "sizes.size"
		}
		if grouped {
			// Group sizes must use only authorized members matching the same
			// filters. A hidden large MOBI cannot reorder a public EPUB card.
			from += ` LEFT JOIN (SELECT k.managed_work,MAX(f.size) size FROM book_identity_keys k JOIN books b ON b.id=k.book_id AND b.work_id=k.managed_work JOIN acl_profile_file f ON f.book_id=b.id WHERE k.managed_work>0 AND (` + baseWhere + `) AND (` + fileWhere + `) GROUP BY k.managed_work) gs ON gs.managed_work=b.work_id`
			pageArgs = append(pageArgs, baseArgs...)
			pageArgs = append(pageArgs, fileArgs...)
		}
		direction := "DESC"
		if q.SizeSort == "asc" {
			direction = "ASC"
		}
		if grouped {
			size = "COALESCE(gs.size," + size + ")"
		}
		order = "(" + size + ">0) DESC," + size + " " + direction + ",b.id DESC"
	}
	if q.RatingSort {
		order = `COALESCE((SELECT MAX(r.rating) FROM books_ratings_link l JOIN ratings r ON r.id=l.rating WHERE l.book=b.id),0) DESC,b.id DESC`
	}
	orderArgs := []any{}
	if q.SearchRelevance && q.Search != "" && q.SizeSort == "" {
		order = `(instr(browse_lower(b.title),?)>0 OR b.id IN (SELECT value FROM json_each(?))) DESC,b.id DESC`
		orderArgs = append(orderArgs, strings.ToLower(strings.TrimSpace(q.Search)), jsonIDs(q.SearchAliasIDs))
	}
	if q.Start < 0 {
		q.Start = 0
	}
	if q.Size < 1 {
		q.Size = 1
	}
	if q.Size > 1000 {
		return nil, 0, fmt.Errorf("page size exceeds limit")
	}
	// The outer profile row already proves book membership. Avoid rebuilding
	// the entire authorized file set for an ordinary indexed page query.
	if !grouped && !fileFiltered {
		where, args = filterWhere, filterArgs
	}
	pageArgs = append(pageArgs, scope.revision, scope.profile, scope.principal)
	pageArgs = append(pageArgs, args...)
	pageArgs = append(pageArgs, orderArgs...)
	pageArgs = append(pageArgs, q.Size, q.Start)
	rows, err := tx.QueryContext(ctx, `SELECT b.id FROM `+from+` WHERE p.revision=? AND p.profile=? AND p.owner IN ('',?) AND (`+where+`) ORDER BY `+order+` LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	if err = (AccessStore{DB: db}).AccessCurrent(ctx, scope); err != nil {
		return nil, 0, err
	}
	return ids, total, nil
}

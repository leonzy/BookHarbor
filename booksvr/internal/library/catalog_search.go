package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

type CatalogSearchQuery struct {
	Keyword string
	Aliases []int64
	Authors []string
	Scope   *AccessScope
	BrowseQuery
}

// A gram match only identifies candidates. The real title checks adjacency
// and makes queued edits immediately visible without showing stale matches.
func titleKeyword(value string) (string, []any) {
	value = strings.ToLower(value)
	usesCase := false
	for _, r := range value {
		if unicode.SimpleFold(r) != r {
			usesCase = true
			break
		}
	}
	check := func(column string) string {
		if usesCase {
			column = "browse_lower(" + column + ")"
		}
		return "instr(" + column + ",?)>0"
	}
	grams := titleGrams(value)
	if len(grams) == 0 {
		return check("b.title"), []any{value}
	}
	encoded, _ := json.Marshal(grams)
	return `b.id IN (
		SELECT book_id FROM book_title_grams WHERE gram IN (SELECT value FROM json_each(?))
		GROUP BY book_id HAVING COUNT(*)=?
		UNION ALL SELECT tail.id FROM book_title_index state
			CROSS JOIN books tail INDEXED BY idx_books_title
			WHERE state.id=1 AND state.cursor<(SELECT max(id) FROM books)-65536
			AND tail.id>state.cursor AND ` + check("tail.title") + `
		UNION ALL SELECT tail.id FROM book_title_index state CROSS JOIN books tail
			WHERE state.id=1 AND state.cursor>=(SELECT max(id) FROM books)-65536
			AND tail.id>state.cursor AND ` + check("tail.title") + `
		UNION ALL SELECT book_id FROM book_title_dirty
	) AND ` + check("b.title"), []any{string(encoded), len(grams), value, value, value}
}

func catalogPredicate(n node, scope *AccessScope) (string, []any, error) {
	combine := func(l, r node, op string) (string, []any, error) {
		a, args, err := catalogPredicate(l, scope)
		if err != nil {
			return "", nil, err
		}
		b, more, err := catalogPredicate(r, scope)
		return "(" + a + ") " + op + " (" + b + ")", append(args, more...), err
	}
	switch v := n.(type) {
	case *andNode:
		return combine(v.left, v.right, "AND")
	case *orNode:
		return combine(v.left, v.right, "OR")
	case *notNode:
		p, args, err := catalogPredicate(v.inner, scope)
		return "NOT (" + p + ")", args, err
	case *termNode:
		if v.field == "title-keyword" {
			p, args := titleKeyword(v.value)
			return p, args, nil
		}
		if scope != nil {
			return accessSearchNode(*scope, v)
		}
	}
	return n.toSQL()
}

// CatalogSearchPage keeps candidate filtering, visibility, work collapse,
// counting and slicing in one SQLite statement and snapshot.
func CatalogSearchPage(ctx context.Context, db *sql.DB, q CatalogSearchQuery) ([]int64, int, error) {
	tokens, err := lexSearch(strings.TrimSpace(q.Keyword), "title-keyword")
	if err != nil {
		return nil, 0, err
	}
	p := parser{tokens: tokens}
	n, err := p.parseExpr()
	if err != nil {
		return nil, 0, err
	}
	if p.pos != len(tokens) {
		return nil, 0, fmt.Errorf("library: 查询语法错误：位置 %d 处有多余内容", p.pos)
	}
	where, filterArgs, err := q.BrowseQuery.where(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	scope := q.Scope
	var joins string
	var joinArgs []any
	size := "0"
	if scope != nil && !scope.Unrestricted() {
		if !scope.valid || scope.profile == "" {
			return nil, 0, ErrAccessNotReady
		}
		// Probe permission by candidate ID, never expand the full profile.
		joins = ` CROSS JOIN acl_profile_book profile ON profile.revision=? AND profile.profile=?
			AND profile.book_id=b.id AND profile.owner IN ('',?)`
		joinArgs = []any{scope.revision, scope.profile, scope.principal}
		size = "profile.size"
	} else {
		scope = nil
		if q.SizeSort != "" {
			joins = " LEFT JOIN book_sort_sizes sz ON sz.book_id=b.id"
			size = "COALESCE(sz.size,0)"
		}
	}
	search, searchArgs, err := catalogPredicate(n, scope)
	if err != nil {
		return nil, 0, err
	}
	if len(q.Aliases) > 0 {
		search = "(" + search + ") OR b.id IN (SELECT value FROM json_each(?))"
		searchArgs = append(searchArgs, jsonIDs(q.Aliases))
	}
	if len(q.Authors) > 0 {
		authors, _ := json.Marshal(q.Authors)
		search = "(" + search + `) OR b.id IN (SELECT l.book FROM authors a
			JOIN books_authors_link l ON l.author=a.id WHERE a.name IN (SELECT value FROM json_each(?)))`
		searchArgs = append(searchArgs, string(authors))
	}
	order := "relevant DESC,id DESC"
	if q.SizeSort == "asc" {
		order = "(group_size>0) DESC,group_size ASC,id DESC"
	} else if q.SizeSort == "desc" {
		order = "(group_size>0) DESC,group_size DESC,id DESC"
	}
	stmt := `WITH matching AS MATERIALIZED (
		SELECT b.id,COALESCE(k.managed_work,0) work,
			(instr(browse_lower(b.title),?)>0 OR b.id IN (SELECT value FROM json_each(?))) relevant,` + size + ` size
		FROM books b` + joins + `
		LEFT JOIN book_identity_keys k ON k.book_id=b.id AND k.managed_work=b.work_id AND k.managed_work>0
		WHERE ` + where + ` AND (` + search + `)
	), ranked AS MATERIALIZED (
		SELECT id,relevant,
			row_number() OVER (PARTITION BY work,CASE WHEN work=0 THEN id ELSE 0 END ORDER BY relevant DESC,id DESC) rn,
			max(size) OVER (PARTITION BY work,CASE WHEN work=0 THEN id ELSE 0 END) group_size
		FROM matching
	), page AS (
		SELECT id,relevant,group_size FROM ranked WHERE rn=1 ORDER BY ` + order + ` LIMIT ? OFFSET ?
	)
	SELECT (SELECT COUNT(*) FROM ranked WHERE rn=1),p.id FROM (SELECT 1) LEFT JOIN page p ON 1 ORDER BY ` + order
	args := []any{strings.ToLower(strings.TrimSpace(q.Keyword)), jsonIDs(q.Aliases)}
	args = append(args, joinArgs...)
	args = append(args, filterArgs...)
	args = append(args, searchArgs...)
	rows, err := db.QueryContext(ctx, stmt, append(args, max(1, q.Size), max(0, q.Start))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var total int
	ids := []int64{}
	for rows.Next() {
		var id sql.NullInt64
		if err := rows.Scan(&total, &id); err != nil {
			return nil, 0, err
		}
		if id.Valid {
			ids = append(ids, id.Int64)
		}
	}
	return ids, total, rows.Err()
}

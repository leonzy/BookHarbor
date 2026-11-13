package library

import (
	"fmt"
	"strings"
)

// Reuse the existing search grammar. Only file-derived predicates change:
// searching for a format must not reveal that a hidden format exists.
func accessSearchPredicate(scope AccessScope, query string) (string, []any, error) {
	tokens, err := lex(strings.TrimSpace(query))
	if err != nil {
		return "", nil, err
	}
	p := &parser{tokens: tokens}
	n, err := p.parseExpr()
	if err != nil {
		return "", nil, err
	}
	if p.pos != len(p.tokens) {
		return "", nil, fmt.Errorf("library: 查询语法错误：有多余内容")
	}
	return accessSearchNode(scope, n)
}

func accessSearchNode(scope AccessScope, n node) (string, []any, error) {
	combine := func(left, right node, op string) (string, []any, error) {
		l, la, err := accessSearchNode(scope, left)
		if err != nil {
			return "", nil, err
		}
		r, ra, err := accessSearchNode(scope, right)
		if err != nil {
			return "", nil, err
		}
		return "(" + l + ") " + op + " (" + r + ")", append(la, ra...), nil
	}
	switch v := n.(type) {
	case *andNode:
		return combine(v.left, v.right, "AND")
	case *orNode:
		return combine(v.left, v.right, "OR")
	case *notNode:
		inner, args, err := accessSearchNode(scope, v.inner)
		return "NOT (" + inner + ")", args, err
	case *termNode:
		if v.field == "format" {
			return `b.id IN (SELECT f.book_id FROM acl_profile_file f WHERE f.revision=? AND f.profile=? AND f.owner IN ('',?) AND f.format=UPPER(?))`, []any{scope.revision, scope.profile, scope.principal, v.value}, nil
		}
		return v.toSQL()
	default:
		return "", nil, fmt.Errorf("library: 不支持的查询表达式")
	}
}

package library

import (
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/normalize"
)

// FileAuthorSupplement only appends explicitly declared coauthors when the source confirms every
// existing known author. The caller must check both manual field locks.
func FileAuthorSupplement(current, incoming []string) []string {
	known := map[string]bool{}
	for _, a := range normalize.CleanAuthors(current) {
		if !localmeta.UnknownAuthor(a) {
			known[normalize.NormalizeAuthor(a)] = true
		}
	}
	declared := map[string]bool{}
	for _, a := range normalize.CleanAuthors(incoming) {
		if !localmeta.UnknownAuthor(a) {
			declared[normalize.NormalizeAuthor(a)] = true
		}
	}
	if len(known) == 0 || len(declared) <= len(known) {
		return nil
	}
	for a := range known {
		if !declared[a] {
			return nil
		}
	}
	out := append([]string{}, current...)
	for _, a := range normalize.CleanAuthors(incoming) {
		key := normalize.NormalizeAuthor(a)
		if !localmeta.UnknownAuthor(a) && !known[key] {
			out = append(out, a)
			known[key] = true
		}
	}
	return out
}

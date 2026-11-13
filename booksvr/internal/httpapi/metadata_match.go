package httpapi

import (
	"regexp"
	"strings"
	"talebook/booksvr/internal/bookidentity"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/normalize"
	"unicode/utf8"
)

var titleDecoration = regexp.MustCompile(`[（(【\[][^(）)】\]]*[）)】\]]`)

func titleForms(title string) []string {
	title = localmeta.FilenameTitle(title)
	clean := titleDecoration.ReplaceAllString(title, "")
	forms := []string{normalizeMetadataName(title), normalizeMetadataName(clean), normalizeMetadataName(localmeta.SearchTitle(title))}
	for _, v := range []string{title, clean} {
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ':' || r == '：' || r == '/' || r == '｜' }) {
			if s := normalizeMetadataName(part); s != "" {
				forms = append(forms, s)
			}
		}
	}
	return forms
}
func relatedTitle(a, b string) bool {
	for _, x := range titleForms(a) {
		for _, y := range titleForms(b) {
			if x == "" || y == "" {
				continue
			}
			if x == y {
				return true
			}
			small, big := x, y
			if utf8.RuneCountInString(small) > utf8.RuneCountInString(big) {
				small, big = big, small
			}
			if utf8.RuneCountInString(small) >= 4 && utf8.RuneCountInString(small)*2 >= utf8.RuneCountInString(big) && strings.Contains(big, small) {
				return true
			}
		}
	}
	return false
}
func metadataAuthor(s string) string {
	s = strings.TrimSpace(titleDecoration.ReplaceAllString(s, ""))
	for _, prefix := range []string{"作者：", "作者:", "著者：", "著者:"} {
		s = strings.TrimPrefix(s, prefix)
	}
	for _, suffix := range []string{"编著", "主编", "著", "编"} {
		s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
	}
	return normalizeMetadataName(s)
}
func consistentMetadataAuthors(a, b []string) bool {
	keys := func(values []string) map[string]bool {
		out := map[string]bool{}
		for _, name := range normalize.CleanAuthors(values) {
			if !localmeta.UnknownAuthor(name) {
				out[metadataAuthor(name)] = true
			}
		}
		return out
	}
	x, y := keys(a), keys(b)
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	subset := func(left, right map[string]bool) bool {
		for name := range left {
			if !right[name] {
				return false
			}
		}
		return true
	}
	return subset(x, y) || subset(y, x)
}

// A search parameter must never turn an unrelated title into a matching edition.
func referenceRelevant(book library.BookData, record map[string]any) bool {
	book = metadataLookupBook(book)
	if !relatedTitle(book.Title, stringOf(record["title"])) {
		return false
	}
	a, b := isbn13(book.ISBN), isbn13(stringOf(record["isbn"]))
	if a != "" && b != "" && a != b {
		return false
	}
	authors := toStringSlice(record["authors"])
	if localmeta.MissingAuthors(book.Authors) || localmeta.MissingAuthors(authors) || consistentMetadataAuthors(book.Authors, authors) {
		return true
	}
	// Reuse the existing strong-ISBN aliases instead of treating a missing
	// Western middle initial as a new author conflict. Bound this extra work.
	if a != "" && a == b && len(book.Authors) <= 16 && len(authors) <= 16 {
		return bookidentity.Compare(bookidentity.Metadata{Title: book.Title, Authors: book.Authors, ISBN: a}, bookidentity.Metadata{Title: stringOf(record["title"]), Authors: authors, ISBN: b}).Work
	}
	return false
}

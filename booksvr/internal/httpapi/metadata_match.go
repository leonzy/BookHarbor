package httpapi

import (
	"regexp"
	"strings"
	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/localmeta"
	"unicode/utf8"
)

var titleDecoration = regexp.MustCompile(`[（(【\[][^(）)】\]]*[）)】\]]`)

func titleForms(title string) []string {
	title = localmeta.FilenameTitle(title)
	clean := titleDecoration.ReplaceAllString(title, "")
	forms := []string{normalizeMetadataName(title), normalizeMetadataName(clean)}
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
func sharedAuthor(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if !localmeta.UnknownAuthor(x) && !localmeta.UnknownAuthor(y) && metadataAuthor(x) == metadataAuthor(y) {
				return true
			}
		}
	}
	return false
}

// A search parameter must never turn an unrelated title into a matching edition.
func referenceRelevant(book library.BookData, record map[string]any) bool {
	if !relatedTitle(book.Title, stringOf(record["title"])) {
		return false
	}
	a, b := isbn13(book.ISBN), isbn13(stringOf(record["isbn"]))
	if a != "" && b != "" {
		return a == b
	}
	authors := toStringSlice(record["authors"])
	return localmeta.MissingAuthors(book.Authors) || localmeta.MissingAuthors(authors) || sharedAuthor(book.Authors, authors)
}

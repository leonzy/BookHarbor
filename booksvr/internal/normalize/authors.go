package normalize

import (
	"golang.org/x/text/unicode/norm"
	"regexp"
	"strings"
	"unicode"
)

var authorSite = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s,;，；、&()（）\[\]【】]+|[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}|(?:[a-z0-9][a-z0-9_\-]*\.)+(?:com|net|org|cn|cc|me|mobi|top|xyz|sk|io|co|info|ru|uk|tk)\b(?:/[^\s,;，；、&()（）\[\]【】]*)?`)
var authorCountry = regexp.MustCompile(`^\s*[\[【(（](?:美|英|法|德|日|俄|意|加|澳|韩|中|奥|美国|英国|法国|德国|日本|俄国|俄罗斯|加拿大|瑞典|西班牙|奥地利|以色列|印度|意大利|澳大利亚|韩国|中国|荷兰|挪威|丹麦|芬兰|瑞士|葡萄牙|比利时|南非)[\]】)）]\s*`)
var authorCountries = regexp.MustCompile(strings.TrimPrefix(authorCountry.String(), `^\s*`))
var authorConjunction = regexp.MustCompile(`(?i)\s+(?:and|with)\s+`)
var authorRoleSeparator = regexp.MustCompile(`(?:编著|著|译|主编|编)[ \t]+`)
var authorTail = regexp.MustCompile(`(?i)(?:\s+等|\s+et\s+al\.?)$`)
var authorLatinAlias = regexp.MustCompile(`[（(][\p{Latin}\p{M}.'’\- ]+[）)]`)
var authorEmptyBrackets = regexp.MustCompile(`\([ \t]*\)|（[ \t]*）|\[[ \t]*\]|【[ \t]*】`)
var authorBy = regexp.MustCompile(`(?i)^(?:by|and)\s+`)
var latinWord = regexp.MustCompile(`^[\p{Latin}\p{M}.'’\- ]+$`)

func latinName(s string) bool { return latinWord.MatchString(strings.TrimSpace(s)) }

// CleanAuthors removes non-author evidence and resolves explicit list separators.
// It does not write metadata; callers decide whether a field may be changed.
func CleanAuthors(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		s := strings.TrimSpace(value)
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "creator:") || strings.HasPrefix(lower, "producer:") {
			continue
		}
		for _, prefix := range []string{"Author:", "author:", "作者：", "作者:"} {
			s = strings.TrimPrefix(s, prefix)
		}
		s = authorSite.ReplaceAllString(s, "")
		s = authorEmptyBrackets.ReplaceAllString(s, "")
		// Keep a leading country label in displayed file metadata. An internal
		// country label starts another credited person; comparison strips either.
		prefix := authorCountry.FindString(s)
		s = prefix + authorCountries.ReplaceAllString(strings.TrimPrefix(s, prefix), ";")
		s = authorConjunction.ReplaceAllString(s, ";")
		s = authorRoleSeparator.ReplaceAllString(s, ";")
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("&＆;；、/|", r) }) {
			part = strings.Trim(part, " \t\r\n,，")
			pieces := strings.FieldsFunc(part, func(r rune) bool { return r == ',' || r == '，' })
			// A single Western surname-first form is one person, not two authors.
			if len(pieces) == 2 && invertedName(pieces[0], pieces[1]) {
				pieces = []string{strings.TrimSpace(pieces[1]) + " " + strings.TrimSpace(pieces[0])}
			}
			for _, name := range pieces {
				name = strings.TrimSpace(authorBy.ReplaceAllString(strings.TrimSpace(name), ""))
				name = strings.TrimSpace(authorTail.ReplaceAllString(name, ""))
				if strings.Trim(name, "()（）[]【】 \t") == "" || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func invertedName(surname, given string) bool {
	if !latinName(surname) || !latinName(given) {
		return false
	}
	left, right := strings.Fields(surname), strings.Fields(given)
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	return len(left) == 1 || len(right) == 1 || strings.HasPrefix(strings.ToLower(strings.TrimSpace(surname)), "de ") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(surname)), "van ")
}

// LatinAuthorAlias is deliberately available only to callers with strong ISBN evidence.
// Missing middle initials are tolerated; different full given names are not.
func LatinAuthorAlias(a, b string) bool {
	a, b = NormalizeAuthor(a), NormalizeAuthor(b)
	if !latinName(a) || !latinName(b) {
		return false
	}
	x, y := strings.Fields(a), strings.Fields(b)
	if len(x) < 2 || len(y) < 2 || len([]rune(x[0])) < 2 || len([]rune(y[0])) < 2 || x[0] != y[0] || x[len(x)-1] != y[len(y)-1] {
		return false
	}
	x, y = x[1:len(x)-1], y[1:len(y)-1]
	if len(x) == 0 || len(y) == 0 {
		for _, middle := range append(x, y...) {
			if len([]rune(middle)) != 1 {
				return false
			}
		}
		return true
	}
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] && !(len([]rune(x[i])) == 1 && strings.HasPrefix(y[i], x[i])) && !(len([]rune(y[i])) == 1 && strings.HasPrefix(x[i], y[i])) {
			return false
		}
	}
	return true
}

func stripAuthorCountry(s string) string {
	return authorCountry.ReplaceAllString(NormalizeWidth(s), "")
}

// Canonical author comparison retains Chinese names when a publisher also gives
// their Latin spelling in parentheses. It does not erase arbitrary annotations.
func authorComparisonText(s string) string {
	han := false
	for _, r := range s {
		han = han || unicode.Is(unicode.Han, r)
	}
	if han {
		s = authorLatinAlias.ReplaceAllString(s, "")
	}
	s = authorTail.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if r == 'ı' {
			return 'i'
		}
		return r
	}, norm.NFD.String(s))
	return s
}

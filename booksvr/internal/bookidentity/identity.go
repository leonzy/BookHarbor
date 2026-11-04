// Package bookidentity compares publication metadata without changing it.
package bookidentity

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/normalize"
)

type Metadata struct {
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	ISBN      string   `json:"isbn"`
	Publisher string   `json:"publisher"`
	Pubdate   string   `json:"pubdate"`
	Language  string   `json:"language"`
}
type Keys struct{ ISBN, Title, Authors, Version string }
type Decision struct {
	Work, Edition bool
	Reason        string
}

// ISBN returns one ISBN-13 for both equivalent ISBN representations.
func ISBN(raw string) string {
	s := localmeta.ISBN(raw)
	if len(s) != 10 {
		return s
	}
	base := "978" + s[:9]
	sum := 0
	for i := range base {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(base[i]-'0') * w
	}
	return base + string(byte('0'+(10-sum%10)%10))
}

// ISBNForms supplies bounded indexed lookup alternatives for legacy rows.
func ISBNForms(raw string) []string {
	canonical := ISBN(raw)
	if canonical == "" {
		return nil
	}
	values := []string{canonical, strings.TrimSpace(raw), localmeta.ISBN(raw)}
	if strings.HasPrefix(canonical, "978") {
		base := canonical[3:12]
		sum := 0
		for i := range base {
			sum += (10 - i) * int(base[i]-'0')
		}
		check := (11 - sum%11) % 11
		c := byte('0' + check)
		if check == 10 {
			c = 'X'
		}
		values = append(values, base+string(c))
	}
	return values
}

var brackets = regexp.MustCompile(`\([^()]*\)|（[^（）]*）|【[^【】]*】|\[[^\[\]]*\]`)
var volume = regexp.MustCompile(`第[0-9一二三四五六七八九十百]+[册卷部集版]|[上中下][册卷部集]|修订版|增订版|全译本|节选本|缩写本|译本|精装版|平装版|套装|合集|全集`)
var series = regexp.MustCompile(`^[^()（）【】\[\]]{1,35}(?:丛书|文库|书系)$`)

func CleanTitle(raw string) string {
	s := localmeta.SearchTitle(raw)
	return strings.TrimSpace(brackets.ReplaceAllStringFunc(s, func(group string) string {
		inner := strings.TrimSpace(string([]rune(group)[1 : len([]rune(group))-1]))
		if volume.MatchString(inner) {
			return group
		}
		if series.MatchString(inner) {
			return ""
		}
		if len([]rune(inner)) >= 12 && (strings.Contains(inner, "推荐") || strings.Contains(inner, "新书") || strings.Contains(inner, "畅销")) && (strings.ContainsAny(inner, "，、！!") || len([]rune(inner)) >= 30) {
			return ""
		}
		return group
	}))
}
func compact(s string) string {
	s = normalize.NormalizeTitle(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}
func authorKeys(authors []string) []string {
	set := map[string]bool{}
	for _, raw := range authors {
		for _, s := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune("&＆;；、", r) }) {
			if localmeta.UnknownAuthor(s) {
				continue
			}
			s = normalize.NormalizeAuthor(s)
			if !localmeta.UnknownAuthor(s) {
				set[s] = true
			}
		}
	}
	values := []string{}
	for s := range set {
		values = append(values, s)
	}
	sort.Strings(values)
	return values
}
func Key(m Metadata) Keys {
	clean := CleanTitle(m.Title)
	markers := volume.FindAllString(normalize.NormalizeWidth(clean), -1)
	sort.Strings(markers)
	return Keys{ISBN(m.ISBN), compact(clean), strings.Join(authorKeys(m.Authors), "\x1f"), strings.Join(markers, "\x1f")}
}
func subset(a, b []string) bool {
	set := map[string]bool{}
	for _, v := range b {
		set[v] = true
	}
	for _, v := range a {
		if !set[v] {
			return false
		}
	}
	return true
}
func language(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Split(strings.ReplaceAll(s, "_", "-"), "-")[0]
	switch s {
	case "zho", "chi":
		return "zh"
	case "eng":
		return "en"
	}
	return s
}
func Compare(a, b Metadata) Decision {
	x, y := Key(a), Key(b)
	sameISBN := x.ISBN != "" && x.ISBN == y.ISBN
	if x.Title == "" || x.Title != y.Title {
		return Decision{Reason: "title_conflict"}
	}
	aa, bb := authorKeys(a.Authors), authorKeys(b.Authors)
	if sameISBN {
		if len(aa) > 0 && len(bb) > 0 && !subset(aa, bb) && !subset(bb, aa) {
			return Decision{Reason: "author_conflict"}
		}
	} else if len(aa) == 0 || len(bb) == 0 || x.Authors != y.Authors {
		return Decision{Reason: "author_insufficient"}
	}
	d := Decision{Work: true, Edition: true, Reason: "title_authors"}
	if sameISBN {
		d.Reason = "isbn"
	}
	if x.Version != y.Version {
		d.Edition = false
		d.Reason = "version_conflict"
		return d
	}
	if language(a.Language) != "" && language(b.Language) != "" && language(a.Language) != language(b.Language) {
		d.Edition = false
		d.Reason = "language_conflict"
		return d
	}
	if x.ISBN != "" && y.ISBN != "" && x.ISBN != y.ISBN {
		d.Edition = false
		d.Reason = "different_isbn"
		return d
	}
	if a.Publisher != "" && b.Publisher != "" && compact(a.Publisher) != compact(b.Publisher) {
		d.Edition = false
		d.Reason = "publisher_conflict"
		return d
	}
	if len(a.Pubdate) >= 4 && len(b.Pubdate) >= 4 && a.Pubdate[:4] != b.Pubdate[:4] {
		d.Edition = false
		d.Reason = "publication_conflict"
	}
	return d
}

package localmeta

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var queryFileExtension = regexp.MustCompile(`(?i)\.(?:txt|epub|mobi|azw3|azw|pdf|fb2|cbz)$`)
var queryFormatTag = regexp.MustCompile(`(?i)^(?:(?:txt|epub|mobi|azw3|pdf|kindle)(?:版|格式)?[ +/,、]*)+$`)
var queryEdition = regexp.MustCompile(`第[0-9一二三四五六七八九十百]+[卷册部集版]|[上中下][卷册部集]|[0-9一二三四五六七八九十百]+[卷册部集]|修订|增订|新版|全译|译本|插图本|精装|平装|纪念版|珍藏版`)

// SearchTitle is a temporary search hint, never a replacement for stored metadata
// or an import title. Unknown parentheses, subtitles and edition identifiers stay.
func SearchTitle(raw string) string {
	title := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch r {
		case '\ufeff', '\u200b', '\u2060':
			return -1
		}
		return r
	}, html.UnescapeString(raw))), " ")
	if title == "" || utf8.RuneCountInString(title) > 1024 {
		return title
	}
	title = FilenameTitle(queryFileExtension.ReplaceAllString(title, ""))
	for pass := 0; pass < 4; pass++ {
		before := title
		if strings.HasPrefix(title, "《") && strings.Contains(title, "》") && strings.Count(title, "《") == 1 && strings.Count(title, "》") == 1 {
			title = strings.TrimSpace(strings.Replace(strings.TrimPrefix(title, "《"), "》", "", 1))
		}
		rs := []rune(title)
		stack := []int{}
		skip := make([]bool, len(rs))
		for end, r := range rs {
			if strings.ContainsRune("(（[【", r) {
				stack = append(stack, end)
				continue
			}
			if !strings.ContainsRune(")）]】", r) || len(stack) == 0 {
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !queryPair(rs[start], r) {
				stack = nil
				continue
			}
			var inner strings.Builder
			for i := start + 1; i < end; i++ {
				if !skip[i] {
					inner.WriteRune(rs[i])
				}
			}
			if !queryNoise(inner.String()) {
				continue
			}
			remaining := 0
			for i, c := range rs {
				if (i < start || i > end) && !skip[i] && (unicode.IsLetter(c) || unicode.IsDigit(c)) {
					remaining++
				}
			}
			if remaining < 2 {
				continue
			}
			for i := start; i <= end; i++ {
				skip[i] = true
			}
		}
		var out strings.Builder
		for i, r := range rs {
			if !skip[i] {
				out.WriteRune(r)
			}
		}
		title = strings.Join(strings.Fields(out.String()), " ")
		if title == before {
			break
		}
	}
	if title == "" {
		return strings.TrimSpace(raw)
	}
	return title
}
func queryPair(a, b rune) bool {
	return a == '(' && b == ')' || a == '（' && b == '）' || a == '[' && b == ']' || a == '【' && b == '】'
}
func queryNoise(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if queryEdition.MatchString(s) {
		return false
	}
	switch s {
	case "校对版全本", "校对全本", "校对版", "精校版", "精校全本", "精排版", "完结", "完本", "完整版", "电子书", "电子版", "无删减全本":
		return true
	}
	if queryFormatTag.MatchString(s) {
		return true
	}
	for _, mark := range []string{"微信公众号：", "微信公众号:", "下载地址：", "下载地址:", "资源整理：", "资源整理:"} {
		if strings.HasPrefix(s, mark) {
			return true
		}
	}
	// Long text alone is not evidence: require recognisable promotional wording.
	if utf8.RuneCountInString(s) < 10 {
		return false
	}
	for _, mark := range []string{"幕后真相", "一手史料", "内容简介", "重磅推荐", "倾情推荐", "联袂推荐", "畅销百万", "畅销千万", "豆瓣高分", "全新力作", "震撼上市", "百万读者", "千万读者"} {
		if strings.Contains(s, mark) {
			return true
		}
	}
	return false
}

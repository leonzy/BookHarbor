package localmeta

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

var synopsisHeading = regexp.MustCompile(`^(?:【(?:内容简介|作品简介|简介|文案)】|\[(?:内容简介|作品简介|简介|文案)\]|(?:内容简介|作品简介|简介|文案)\s*[:：])(.*)$`)
var synopsisBoundary = regexp.MustCompile(`^(?:第[0-9零〇一二三四五六七八九十百千万两]+[章节回卷部]|(?:相关设定|作品相关|目录|正文|序章|楔子|版权信息|作者简介)(?:\s|$)|[-=＊*]{3,})`)
var namedFileTitle = regexp.MustCompile(`^《([^《》\r\n]+)》(?:[（(][^）)\r\n]{0,40}[）)])?\s*(?:作者|著者)\s*[:：]\s*[^\r\n]+$`)

// FilenameTitle only strips an unambiguous, explicitly author-labelled wrapper.
// It is a lookup/new-import hint; it never rewrites an existing library title.
func FilenameTitle(name string) string {
	if m := namedFileTitle.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return name
}

// synopsis requires a labelled block before the first chapter. It does not
// promote arbitrary novel text or a download banner to book metadata.
func synopsis(text string) string {
	truncated := len(text) > 64<<10
	if truncated {
		text = text[:64<<10]
	}
	var lines []string
	active, size := false, 0
	rows := strings.Split(text, "\n")
	for i, raw := range rows {
		if truncated && i == len(rows)-1 {
			return ""
		}
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if synopsisBoundary.MatchString(line) {
			if active || strings.HasPrefix(line, "第") {
				break
			}
			continue
		}
		if !active {
			match := synopsisHeading.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			active = true
			line = strings.TrimSpace(match[1])
		}
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		// Reject incomplete/oversized blocks rather than saving truncated prose.
		size += utf8.RuneCountInString(line)
		if size > 4000 || !utf8.ValidString(line) {
			return ""
		}
		lines = append(lines, html.EscapeString(line))
	}
	return strings.Join(lines, "\n")
}

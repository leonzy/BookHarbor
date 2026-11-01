package httpapi

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
)

func decodeTxtBody(body []byte) string {
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if bytes.HasPrefix(body, []byte{0xff, 0xfe}) || bytes.HasPrefix(body, []byte{0xfe, 0xff}) {
		decoded, err := textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM).NewDecoder().Bytes(body)
		if err == nil {
			return string(decoded)
		}
	}
	if utf8.Valid(body) {
		return string(body)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(body)
	if err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(body), "�")
}

var txtChapterHeading = regexp.MustCompile(`^(?:第[零〇一二三四五六七八九十百千万两0-9]+[章节回卷部篇集]|[Cc][Hh][Aa][Pp][Tt][Ee][Rr]\s+[0-9]+|序章|楔子|后记|尾声)`)

// Offsets refer to the normalized UTF-8 text used by the chapter endpoint.
func txtChapters(text string) []map[string]any {
	printed := txtPrintedTOC(text)
	chapters := []map[string]any{{"title": "正文", "start": 0, "end": len(text)}}
	offset := 0
	lastHeadingEnd := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		title := strings.TrimSpace(line)
		last := chapters[len(chapters)-1]
		heading := !printed[offset] && utf8.RuneCountInString(title) <= 100 && txtChapterHeading.MatchString(title)
		chunk := offset-last["start"].(int) >= 32000 && title != ""
		if heading || chunk {
			if heading && len(printed) > 0 && lastHeadingEnd > last["start"].(int) && strings.TrimSpace(text[lastHeadingEnd:offset]) == "" {
				lastHeadingEnd = offset + len(line)
				offset += len(line)
				continue
			}
			if !heading {
				title = "正文（续）"
			}
			if heading {
				lastHeadingEnd = offset + len(line)
			} else {
				lastHeadingEnd = 0
			}
			if offset == 0 {
				last["title"] = html.EscapeString(title)
			} else {
				last["end"] = offset
				chapters = append(chapters, map[string]any{"title": html.EscapeString(title), "start": offset, "end": len(text)})
			}
		}
		offset += len(line)
	}
	return chapters
}

// Ignore navigation duplicates, never bytes from the source. Dense, mostly
// empty front matter needs multiple later chapters with substantial text.
func txtPrintedTOC(text string) map[int]bool {
	type heading struct {
		start, end              int
		key                     string
		empty, repeated, strong bool
	}
	var hs []heading
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		title := strings.TrimSpace(line)
		if utf8.RuneCountInString(title) <= 100 && txtChapterHeading.MatchString(title) {
			hs = append(hs, heading{start: offset, end: offset + len(line), key: strings.Join(strings.Fields(title), "")})
		}
		offset += len(line)
	}
	seen, substantial := map[string]bool{}, map[string]bool{}
	for i := len(hs) - 1; i >= 0; i-- {
		h := &hs[i]
		end := len(text)
		if i+1 < len(hs) {
			end = hs[i+1].start
		}
		body := text[h.end:end]
		h.empty = strings.TrimSpace(body) == ""
		h.repeated = seen[h.key]
		h.strong = substantial[h.key]
		seen[h.key] = true
		n := 0
		for _, r := range body {
			if !unicode.IsSpace(r) {
				n++
			}
			if n >= 80 {
				break
			}
		}
		if n >= 80 {
			substantial[h.key] = true
		}
	}
	ignored := map[int]bool{}
	for i := 0; i < len(hs); {
		if !hs[i].repeated {
			i++
			continue
		}
		start, empty, strong := i, 0, 0
		for i < len(hs) && hs[i].repeated {
			if hs[i].empty {
				empty++
			}
			if hs[i].strong {
				strong++
			}
			i++
		}
		if i-start >= 3 && strong >= 3 && empty*3 >= (i-start)*2 && hs[start].start < 64<<10 {
			for j := start; j < i; j++ {
				ignored[hs[j].start] = true
			}
		}
	}
	return ignored
}

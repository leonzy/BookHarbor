// Package localmeta fills gaps from bounded, read-only copyright/text inspection.
package localmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/simplifiedchinese"
	"talebook/booksvr/internal/epubpath"
	"talebook/booksvr/internal/importmeta"
)

const TextLimit = 512 << 10

// ISBN rejects arbitrary identifiers and validates both ISBN check digits.
func ISBN(s string) string {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimPrefix(s, "URN:ISBN:")
	s = strings.TrimPrefix(s, "ISBN-13:")
	s = strings.TrimPrefix(s, "ISBN-10:")
	s = strings.TrimPrefix(s, "ISBN:")
	s = strings.Map(func(r rune) rune {
		if r == '-' || r == '－' || r == ' ' || r == '\u3000' || r == '\u00a0' {
			return -1
		}
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}, strings.TrimSpace(s))
	sum := 0
	if s == "0000000000" {
		return ""
	}
	if len(s) == 13 && (strings.HasPrefix(s, "978") || strings.HasPrefix(s, "979")) {
		for i := 0; i < 13; i++ {
			if s[i] < '0' || s[i] > '9' {
				return ""
			}
			w := 1
			if i%2 == 1 {
				w = 3
			}
			sum += int(s[i]-'0') * w
		}
		if sum%10 == 0 {
			return s
		}
	}
	if len(s) == 10 {
		for i := 0; i < 10; i++ {
			v := int(s[i] - '0')
			if i == 9 && s[i] == 'X' {
				v = 10
			} else if s[i] < '0' || s[i] > '9' {
				return ""
			}
			sum += (10 - i) * v
		}
		if sum%11 == 0 {
			return s
		}
	}
	return ""
}

func UnknownAuthor(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "佚名", "未知", "未知作者", "不详", "unknown", "unknown author", "anonymous":
		return true
	}
	return false
}
func MissingAuthors(a []string) bool {
	for _, s := range a {
		if !UnknownAuthor(s) {
			return false
		}
	}
	return true
}

var labels = regexp.MustCompile(`(?im)(ISBN(?:[-－](?:10|13))?|书名|作者|著者|作\s*者|出版社|出版发行|出版单位|出版时间|出版日期|Title|Author|Publisher)\s*[:：]\s*`)
var isbnText = regexp.MustCompile(`(?i)(?:ISBN(?:[-－](?:10|13))?|国际标准书号)\s*[:：]?\s*((?:97[89][\s-]*)?[0-9０-９][0-9０-９Xx\s-]{8,25})`)

// FromText only trusts explicitly labelled fields, never free-form prose guesses.
func FromText(text string) importmeta.Metadata {
	var m importmeta.Metadata
	text = strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		if r == '－' {
			return '-'
		}
		return r
	}, text)
	text = strings.ReplaceAll(text, "\r", "\n")
	matches := labels.FindAllStringSubmatchIndex(text, -1)
	for i, p := range matches {
		end := len(text)
		if n := strings.IndexByte(text[p[1]:], '\n'); n >= 0 {
			end = p[1] + n
		}
		if i+1 < len(matches) && matches[i+1][0] < end {
			end = matches[i+1][0]
		}
		v := strings.TrimSpace(text[p[1]:end])
		if v == "" || utf8.RuneCountInString(v) > 160 {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(text[p[2]:p[3]], " ", ""))
		switch key {
		case "书名", "title":
			if m.Title == "" {
				m.Title = v
			}
		case "作者", "著者", "author":
			if len(m.Authors) == 0 && utf8.RuneCountInString(v) <= 80 && !UnknownAuthor(v) {
				m.Authors = []string{v}
			}
		case "出版社", "出版发行", "出版单位", "publisher":
			if m.Publisher == "" && ISBN(v) == "" {
				m.Publisher = v
			}
		case "出版时间", "出版日期":
			if m.Pubdate == "" {
				m.Pubdate = v
			}
		}
	}
	for _, p := range isbnText.FindAllStringSubmatch(text, -1) {
		s := strings.TrimSpace(p[1])
		lengths := []int{13, 10}
		if strings.HasPrefix(strings.ReplaceAll(s, "-", ""), "978") || strings.HasPrefix(strings.ReplaceAll(s, "-", ""), "979") {
			lengths = []int{13}
		}
		for _, n := range lengths {
			clean := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) || r == '-' {
					return -1
				}
				return r
			}, s)
			if len(clean) >= n {
				if v := ISBN(clean[:n]); v != "" {
					m.ISBN = v
					break
				}
			}
		}
		if m.ISBN != "" {
			break
		}
	}
	return m
}

// Merge never replaces a higher-priority value with a lower-priority candidate.
func Merge(m *importmeta.Metadata, other importmeta.Metadata) {
	if m.Title == "" {
		m.Title = other.Title
	}
	if MissingAuthors(m.Authors) && !MissingAuthors(other.Authors) {
		m.Authors = other.Authors
	}
	if m.Publisher == "" {
		m.Publisher = other.Publisher
	}
	if m.ISBN == "" {
		m.ISBN = ISBN(other.ISBN)
	}
	if m.Pubdate == "" {
		m.Pubdate = other.Pubdate
	}
	if m.Language == "" {
		m.Language = other.Language
	}
	if m.Comments == "" {
		m.Comments = other.Comments
	}
	if len(m.Tags) == 0 {
		m.Tags = other.Tags
	}
}

func visible(data []byte) string {
	if !utf8.Valid(data) {
		data, _ = simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	}
	root, e := html.Parse(bytes.NewReader(data))
	if e != nil {
		return string(data)
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "br", "li", "h1", "h2", "h3", "tr", "section":
				out.WriteByte('\n')
			}
		}
	}
	walk(root)
	return out.String()
}

func readZipEntry(f *zip.File, limit int) []byte {
	if f.UncompressedSize64 > uint64(limit) {
		return nil
	}
	r, e := f.Open()
	if e != nil {
		return nil
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if e != nil || len(b) > limit {
		return nil
	}
	return b
}

func epubText(path string) string {
	z, e := zip.OpenReader(path)
	if e != nil {
		return ""
	}
	defer z.Close()
	var priority, other []*zip.File
	for _, f := range z.File {
		n := strings.ToLower(f.Name)
		ext := filepath.Ext(n)
		if ext != ".html" && ext != ".xhtml" && ext != ".htm" {
			continue
		}
		if strings.Contains(n, "copyright") || strings.Contains(n, "colophon") || strings.Contains(n, "版权") || strings.Contains(n, "bookinfo") || strings.Contains(n, "titlepage") {
			priority = append(priority, f)
		} else {
			other = append(other, f)
		}
	}
	var out strings.Builder
	// Explicit copyright pages precede the package's actual reading order.
	ordered := epubReadingOrder(z.File)
	if len(ordered) > 0 {
		other = ordered
	}
	seen := map[string]bool{}
	for i, f := range append(priority, other...) {
		if i >= 12 || out.Len() >= TextLimit {
			break
		}
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		b := readZipEntry(f, 128<<10)
		if len(b) > 0 {
			v := visible(b)
			remaining := TextLimit - out.Len()
			if len(v) > remaining {
				v = v[:remaining]
			}
			out.WriteString(v)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func epubReadingOrder(files []*zip.File) []*zip.File {
	container := epubpath.Find(files, "META-INF/container.xml")
	if container == nil {
		return nil
	}
	var c struct {
		Roots []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(readZipEntry(container, 64<<10), &c) != nil || len(c.Roots) == 0 {
		return nil
	}
	packagePath := c.Roots[0].Path
	opf := epubpath.Find(files, packagePath)
	if opf == nil {
		return nil
	}
	var p struct {
		Manifest []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			ID string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if xml.Unmarshal(readZipEntry(opf, 1<<20), &p) != nil {
		return nil
	}
	items := map[string]string{}
	for _, i := range p.Manifest {
		items[i.ID] = i.Href
	}
	var ordered []*zip.File
	for _, i := range p.Spine {
		href := items[i.ID]
		if href == "" {
			continue
		}
		f := epubpath.Find(files, path.Join(path.Dir(packagePath), strings.Split(href, "#")[0]))
		if f != nil {
			ordered = append(ordered, f)
		}
		if len(ordered) >= 12 {
			break
		}
	}
	return ordered
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, name string, args ...string) []byte {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	b := &cappedBuffer{limit: TextLimit}
	cmd.Stdout = b
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil
	}
	return b.Bytes()
}

// PDF reads metadata properties first; text extraction is bounded to eight pages.
func pdf(ctx context.Context, path string) importmeta.Metadata {
	var m importmeta.Metadata
	b := command(ctx, "ebook-meta", path, "--as-opf")
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, e := dec.Token()
		if e != nil {
			break
		}
		if n, ok := tok.(xml.StartElement); ok {
			switch n.Name.Local {
			case "creator", "publisher", "identifier", "language":
				var v string
				if dec.DecodeElement(&v, &n) == nil {
					switch n.Name.Local {
					case "creator":
						if !UnknownAuthor(v) {
							m.Authors = append(m.Authors, v)
						}
					case "publisher":
						m.Publisher = v
					case "identifier":
						if v = ISBN(v); v != "" {
							m.ISBN = v
						}
					case "language":
						m.Language = v
					}
				}
			}
		}
	}
	// The production image ships Poppler without the full ebook-meta executable.
	// pdfinfo reads the Info dictionary; XMP supplies publisher/identifier when present.
	if MissingAuthors(m.Authors) {
		Merge(&m, FromText(string(command(ctx, "pdfinfo", path))))
	}
	xmp := command(ctx, "pdfinfo", "-meta", path)
	xd := xml.NewDecoder(bytes.NewReader(xmp))
	for {
		tok, e := xd.Token()
		if e != nil {
			break
		}
		if n, ok := tok.(xml.StartElement); ok {
			switch n.Name.Local {
			case "creator", "publisher", "identifier":
				var inner struct {
					Raw string `xml:",innerxml"`
				}
				if xd.DecodeElement(&inner, &n) == nil {
					v := strings.TrimSpace(visible([]byte(inner.Raw)))
					switch n.Name.Local {
					case "creator":
						if MissingAuthors(m.Authors) && !UnknownAuthor(v) {
							m.Authors = []string{v}
						}
					case "publisher":
						if m.Publisher == "" {
							m.Publisher = v
						}
					case "identifier":
						if m.ISBN == "" {
							m.ISBN = ISBN(v)
						}
					}
				}
			}
		}
	}
	if m.ISBN == "" || MissingAuthors(m.Authors) || m.Publisher == "" {
		Merge(&m, FromText(string(command(ctx, "pdftotext", "-f", "1", "-l", "8", "-nopgbrk", path, "-"))))
	}
	return m
}

// palmText handles uncompressed/PalmDOC text records without converting the book.
func palmText(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 78)
	if _, e = io.ReadFull(f, head); e != nil {
		return ""
	}
	n := int(binary.BigEndian.Uint16(head[76:]))
	if n < 2 {
		return ""
	}
	table := make([]byte, n*8)
	if _, e = io.ReadFull(f, table); e != nil {
		return ""
	}
	off := int64(binary.BigEndian.Uint32(table))
	h := make([]byte, 16)
	if _, e = f.ReadAt(h, off); e != nil {
		return ""
	}
	compression := binary.BigEndian.Uint16(h)
	if (compression != 1 && compression != 2) || binary.BigEndian.Uint16(h[12:]) != 0 {
		return ""
	}
	count := int(binary.BigEndian.Uint16(h[8:]))
	var out []byte
	for i := 1; i < n && i <= count && i <= 32 && len(out) < TextLimit; i++ {
		start := int64(binary.BigEndian.Uint32(table[i*8:]))
		end := start + 65536
		if i+1 < n {
			end = int64(binary.BigEndian.Uint32(table[(i+1)*8:]))
		}
		if end <= start || end-start > 65536 {
			break
		}
		b := make([]byte, end-start)
		nr, _ := f.ReadAt(b, start)
		b = b[:nr]
		if compression == 1 {
			out = append(out, b...)
			continue
		}
		for p := 0; p < len(b) && len(out) < TextLimit; {
			v := b[p]
			p++
			switch {
			case v >= 1 && v <= 8:
				k := int(v)
				if p+k > len(b) {
					p = len(b)
					break
				}
				out = append(out, b[p:p+k]...)
				p += k
			case v <= 127:
				out = append(out, v)
			case v >= 192:
				out = append(out, ' ', v^128)
			default:
				if p >= len(b) {
					break
				}
				pair := int(v)<<8 | int(b[p])
				p++
				distance := (pair >> 3) & 2047
				length := (pair & 7) + 3
				if distance == 0 || distance > len(out) {
					return visible(out)
				}
				for j := 0; j < length; j++ {
					out = append(out, out[len(out)-distance])
				}
			}
		}
	}
	return visible(out)
}

func Supplement(ctx context.Context, path, format string, m importmeta.Metadata) importmeta.Metadata {
	m.ISBN = ISBN(m.ISBN)
	// An ISBN in the publisher slot is a common malformed package, not a publisher.
	if v := ISBN(m.Publisher); v != "" {
		if m.ISBN == "" {
			m.ISBN = v
		}
		m.Publisher = ""
	}
	if m.ISBN == "" || MissingAuthors(m.Authors) || m.Publisher == "" {
		switch strings.ToLower(format) {
		case "epub":
			Merge(&m, FromText(epubText(path)))
		case "mobi", "azw", "azw3":
			Merge(&m, FromText(palmText(path)))
		case "pdf":
			Merge(&m, pdf(ctx, path))
		case "txt":
			f, e := os.Open(path)
			if e == nil {
				b, _ := io.ReadAll(io.LimitReader(f, TextLimit))
				f.Close()
				if !bytes.Contains(b, []byte{0}) {
					Merge(&m, FromText(visible(b)))
				}
			}
		}
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// Filename is last, and only explicit labels supply author/ISBN hints.
	Merge(&m, FromText(name))
	if m.Title == "" {
		m.Title = name
	}
	if MissingAuthors(m.Authors) {
		m.Authors = []string{importmeta.AnonymousAuthor}
	}
	return m
}

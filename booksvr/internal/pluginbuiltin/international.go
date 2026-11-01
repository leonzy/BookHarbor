package pluginbuiltin

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	_ "golang.org/x/image/webp"
	"html"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"regexp"
	"strings"
	"talebook/booksvr/internal/htmlx"
	"talebook/booksvr/internal/localmeta"
	"talebook/booksvr/internal/metabase"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/safehttp"
	"time"
)

// Separate instances use independent durable source queues and backoff.
type internationalProvider struct {
	*metabase.MetaSourceMixin
	amazon bool
	get    func(context.Context, string) ([]byte, error)
}

func newInternationalProvider(amazon bool) *internationalProvider {
	p := &internationalProvider{amazon: amazon}
	p.MetaSourceMixin = metabase.NewMetaSourceMixin(p)
	p.get = p.download
	return p
}
func (p *internationalProvider) Manifest() map[string]any {
	id, name, home := "bookharbor.meta.google-books", "Google Books", "https://books.google.com/"
	if p.amazon {
		id, name, home = "bookharbor.meta.amazon", "Amazon", "https://www.amazon.com/"
	}
	description := "检索 Google Books 的版本、作者、出版信息和封面；独立限速与失败退避。替代旧 Calibre 元数据入口。"
	if p.amazon {
		description = "检索 Amazon 公开书籍页面及封面；访问受限时显示来源失败并退避。替代旧 Calibre 元数据入口。"
	}
	return metabase.MetaManifest(id, name, description, "mdi-book-search", home, metabase.ManifestOptions{})
}
func (p *internationalProvider) ReplacesBuiltin() []string { return []string{"talebook.meta.calibre"} }
func (p *internationalProvider) ProxyImageHosts() []string {
	if p.amazon {
		return []string{"media-amazon.com", "ssl-images-amazon.com", "images.amazon.com"}
	}
	return []string{"books.google.com", "books.googleusercontent.com"}
}
func (p *internationalProvider) trustedCover(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Hostname() == "" || u.Port() != "" && u.Port() != "443" {
		return ""
	}
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	if u.Scheme != "https" {
		return ""
	}
	for _, h := range p.ProxyImageHosts() {
		if u.Hostname() == h || strings.HasSuffix(u.Hostname(), "."+h) {
			return u.String()
		}
	}
	return ""
}
func (p *internationalProvider) download(ctx context.Context, raw string) ([]byte, error) {
	c := &safehttp.SafeHttpClient{EnforcePublicAddress: true, MaxBytes: 6 << 20}
	r, e := c.Get(ctx, raw, safehttp.RequestOptions{Headers: map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"}, Timeout: 12 * time.Second, HasTimeout: true})
	if e != nil {
		return nil, fmt.Errorf("%s 请求失败: %w", p.Manifest()["name"], e)
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, (6<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 6<<20 {
		return nil, fmt.Errorf("来源响应超过大小限制")
	}
	return b, nil
}
func (p *internationalProvider) GetCover(raw string, _ map[string]any) (*pluginiface.CoverResult, error) {
	raw = p.trustedCover(raw)
	if raw == "" {
		return nil, fmt.Errorf("不可信的封面地址")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, e := p.get(ctx, raw)
	if e != nil {
		return nil, e
	}
	_, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return nil, fmt.Errorf("封面不是支持的图片: %w", e)
	}
	if format == "jpeg" {
		format = "jpg"
	}
	return &pluginiface.CoverResult{Extension: format, Content: b}, nil
}
func (p *internationalProvider) SearchBooks(q plugindomain.MetadataQuery, c map[string]any) ([]plugindomain.BookMetadata, error) {
	return p.MetaSourceMixin.SearchBooks(q, c)
}
func (p *internationalProvider) Invoke(m string, a []any, c map[string]any) (any, error) {
	return DispatchInvoke(p, m, a, c)
}
func (p *internationalProvider) Search(q plugindomain.MetadataQuery, _ map[string]any) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if p.amazon {
		return p.amazonSearch(ctx, q)
	}
	keyword := "intitle:" + q.Title
	if len(q.Authors) > 0 {
		keyword += " inauthor:" + strings.Join(q.Authors, " ")
	}
	if isbn := localmeta.ISBN(q.ISBN); isbn != "" {
		keyword = "isbn:" + isbn
	}
	if strings.TrimSpace(q.Title) == "" && localmeta.ISBN(q.ISBN) == "" {
		return nil, nil
	}
	b, e := p.get(ctx, "https://www.googleapis.com/books/v1/volumes?maxResults=5&q="+url.QueryEscape(keyword))
	if e == nil {
		var feed struct {
			Items      []googleVolume  `json:"items"`
			TotalItems *int            `json:"totalItems"`
			Error      json.RawMessage `json:"error"`
		}
		e = json.Unmarshal(b, &feed)
		if e == nil && len(feed.Error) == 0 && (feed.Items != nil || feed.TotalItems != nil) {
			out := []map[string]any{}
			for _, v := range feed.Items {
				if m := p.googleMap(v); m != nil {
					out = append(out, m)
				}
			}
			return out, nil
		}
	}
	// The public Atom feed remains usable when anonymous REST quota is exhausted.
	b, e = p.get(ctx, "https://books.google.com/books/feeds/volumes?max-results=5&start-index=1&min-viewability=none&q="+url.QueryEscape(keyword))
	if e != nil {
		return nil, e
	}
	var feed struct {
		XMLName xml.Name     `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []googleAtom `xml:"entry"`
	}
	if e = xml.Unmarshal(b, &feed); e != nil {
		return nil, fmt.Errorf("Google Books 返回无效目录: %w", e)
	}
	out := []map[string]any{}
	for _, v := range feed.Entries {
		if m := p.atomMap(v); m != nil {
			out = append(out, m)
		}
	}
	return out, nil
}

var volumeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var asinID = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func (p *internationalProvider) Fetch(id string, _ map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if p.amazon {
		if !asinID.MatchString(id) {
			return nil, fmt.Errorf("无效的 Amazon ASIN")
		}
		return p.amazonDetail(ctx, id)
	}
	if !volumeID.MatchString(id) {
		return nil, fmt.Errorf("无效的 Google Books 图书标识")
	}
	b, e := p.get(ctx, "https://www.googleapis.com/books/v1/volumes/"+id)
	if e == nil {
		var v googleVolume
		if json.Unmarshal(b, &v) == nil && v.ID == id {
			if m := p.googleMap(v); m != nil {
				return m, nil
			}
		}
	}
	b, e = p.get(ctx, "https://books.google.com/books/feeds/volumes/"+id)
	if e != nil {
		return nil, e
	}
	var v googleAtom
	if e = xml.Unmarshal(b, &v); e != nil {
		return nil, e
	}
	m := p.atomMap(v)
	if m == nil || m["provider_value"] != id {
		return nil, fmt.Errorf("Google Books 详情标识不匹配")
	}
	return m, nil
}

type googleVolume struct {
	ID   string `json:"id"`
	Info struct {
		Title       string   `json:"title"`
		Authors     []string `json:"authors"`
		Publisher   string   `json:"publisher"`
		Date        string   `json:"publishedDate"`
		Description string   `json:"description"`
		Language    string   `json:"language"`
		Tags        []string `json:"categories"`
		Rating      float64  `json:"averageRating"`
		Identifiers []struct {
			Type  string `json:"type"`
			Value string `json:"identifier"`
		} `json:"industryIdentifiers"`
		Images struct {
			Thumbnail string `json:"thumbnail"`
			Small     string `json:"smallThumbnail"`
		} `json:"imageLinks"`
	} `json:"volumeInfo"`
}

func (p *internationalProvider) googleMap(v googleVolume) map[string]any {
	if !volumeID.MatchString(v.ID) || v.Info.Title == "" {
		return nil
	}
	isbn := ""
	for _, i := range v.Info.Identifiers {
		if i.Type == "ISBN_13" || i.Type == "ISBN_10" {
			if s := localmeta.ISBN(i.Value); s != "" {
				isbn = s
				if len(s) == 13 {
					break
				}
			}
		}
	}
	cover := v.Info.Images.Thumbnail
	if cover == "" {
		cover = v.Info.Images.Small
	}
	return map[string]any{"provider_value": v.ID, "title": v.Info.Title, "authors": v.Info.Authors, "publisher": v.Info.Publisher, "isbn": isbn, "comments": plainInternational(v.Info.Description), "pubdate": v.Info.Date, "language": v.Info.Language, "tags": v.Info.Tags, "rating": v.Info.Rating * 2, "cover_url": p.trustedCover(cover), "website": "https://books.google.com/books?id=" + v.ID, "source": "Google Books"}
}

type googleAtom struct {
	XMLName     xml.Name `xml:"http://www.w3.org/2005/Atom entry"`
	ID          string   `xml:"id"`
	Titles      []string `xml:"http://purl.org/dc/terms title"`
	Authors     []string `xml:"http://purl.org/dc/terms creator"`
	Publisher   string   `xml:"http://purl.org/dc/terms publisher"`
	Date        string   `xml:"http://purl.org/dc/terms date"`
	Description string   `xml:"http://purl.org/dc/terms description"`
	Identifiers []string `xml:"http://purl.org/dc/terms identifier"`
	Links       []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func (p *internationalProvider) atomMap(v googleAtom) map[string]any {
	id := v.ID[strings.LastIndex(v.ID, "/")+1:]
	if !volumeID.MatchString(id) || len(v.Titles) == 0 {
		return nil
	}
	isbn, cover := "", ""
	for _, s := range v.Identifiers {
		if strings.HasPrefix(s, "ISBN:") {
			if n := localmeta.ISBN(strings.TrimPrefix(s, "ISBN:")); n != "" {
				isbn = n
				if len(n) == 13 {
					break
				}
			}
		}
	}
	for _, l := range v.Links {
		if l.Rel == "http://schemas.google.com/books/2008/thumbnail" {
			cover = p.trustedCover(l.Href)
		}
	}
	return map[string]any{"provider_value": id, "title": strings.Join(v.Titles, ": "), "authors": v.Authors, "publisher": v.Publisher, "isbn": isbn, "comments": plainInternational(v.Description), "pubdate": v.Date, "cover_url": cover, "source": "Google Books", "website": "https://books.google.com/books?id=" + id}
}
func plainInternational(s string) string {
	d, e := htmlx.Parse(s)
	if e != nil {
		return ""
	}
	return html.EscapeString(strings.Join(strings.Fields(d.TextSep(" ")), " "))
}
func nodeText(n *htmlx.Node, sel string) string {
	if x := n.SelectOne(sel); x != nil {
		return strings.TrimSpace(x.TextSep(" "))
	}
	return ""
}
func amazonPage(b []byte) (*htmlx.Node, error) {
	s := string(b)
	if strings.Contains(s, "captchacharacters") || strings.Contains(s, "/errors/validateCaptcha") || strings.Contains(s, "<title>Robot Check") || strings.Contains(s, "api-services-support@amazon.com") || strings.Contains(s, "automated access to Amazon") {
		return nil, fmt.Errorf("Amazon 拒绝自动访问或要求验证，稍后重试")
	}
	return htmlx.Parse(s)
}

// A print book's ISBN-10 is a commonly published Amazon product identifier.
// It is only a lookup hint: the returned page must independently confirm its ISBN.
func amazonISBNIdentifier(raw string) string {
	s := localmeta.ISBN(raw)
	if len(s) == 10 {
		return s
	}
	if len(s) != 13 || !strings.HasPrefix(s, "978") {
		return ""
	}
	stem := s[3:12]
	sum := 0
	for i, c := range stem {
		sum += (10 - i) * int(c-'0')
	}
	check := (11 - sum%11) % 11
	if check == 10 {
		return stem + "X"
	}
	return stem + fmt.Sprint(check)
}

func (p *internationalProvider) amazonSearch(ctx context.Context, q plugindomain.MetadataQuery) ([]map[string]any, error) {
	if id := amazonISBNIdentifier(q.ISBN); id != "" {
		if m, e := p.amazonDetail(ctx, id); e == nil && amazonISBNIdentifier(asString(m["isbn"])) == id {
			return []map[string]any{m}, nil
		}
	}

	keyword := strings.TrimSpace(q.Title + " " + strings.Join(q.Authors, " "))
	if isbn := localmeta.ISBN(q.ISBN); isbn != "" {
		keyword = isbn
	}
	if keyword == "" {
		return nil, nil
	}
	params := url.Values{"search-alias": {"stripbooks"}, "unfiltered": {"1"}, "sort": {"relevanceexprank"}}
	if isbn := localmeta.ISBN(q.ISBN); isbn != "" {
		params.Set("search-alias", "aps")
		params.Set("field-isbn", isbn)
	} else {
		params.Set("field-title", q.Title)
		if len(q.Authors) > 0 {
			params.Set("field-author", strings.Join(q.Authors, " "))
		}
	}
	b, e := p.get(ctx, "https://www.amazon.com/s/?"+params.Encode())
	if e != nil {
		return nil, e
	}
	d, e := amazonPage(b)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	var detailErr error
	attempts := 0
	for _, n := range d.Select(`[data-component-type="s-search-result"]`) {
		id := n.Attr("data-asin")
		if !asinID.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		attempts++
		if attempts > 3 {
			break
		}
		m, e := p.amazonDetail(ctx, id)
		if e != nil {
			detailErr = e
			continue
		}
		if m != nil {
			out = append(out, m)
		}
		if len(out) >= 3 {
			break
		}
	}
	if len(out) == 0 && detailErr != nil {
		return nil, detailErr
	}
	if len(out) == 0 && !strings.Contains(strings.ToLower(string(b)), "no results") && !strings.Contains(string(b), "s-search-results") {
		return nil, fmt.Errorf("Amazon 搜索页面无法识别，未当作无匹配处理")
	}
	return out, nil
}
func (p *internationalProvider) amazonDetail(ctx context.Context, id string) (map[string]any, error) {
	b, e := p.get(ctx, "https://www.amazon.com/dp/"+id)
	if e != nil {
		return nil, e
	}
	d, e := amazonPage(b)
	if e != nil {
		return nil, e
	}
	if asin := d.SelectOne("#ASIN"); asin != nil && asin.Attr("value") != "" && asin.Attr("value") != id {
		return nil, fmt.Errorf("Amazon 详情版本标识不匹配")
	}
	title := nodeText(d, "#productTitle")
	if title == "" {
		title = nodeText(d, "#ebooksProductTitle")
	}
	if title == "" {
		return nil, fmt.Errorf("Amazon 详情 %s 缺少书名", id)
	}
	m := map[string]any{"provider_value": id, "title": title, "source": "Amazon", "website": "https://www.amazon.com/dp/" + id}
	authors := []string{}
	seen := map[string]bool{}
	for _, a := range d.Select("#bylineInfo a.a-link-normal") {
		s := strings.TrimSpace(a.Text())
		if s != "" && !strings.Contains(s, "Format") && !seen[s] {
			seen[s] = true
			authors = append(authors, s)
		}
	}
	m["authors"] = authors
	for _, sel := range []string{"#detailBullets_feature_div li", "#productDetails_detailBullets_sections1 tr", "#productDetails_techSpec_section_1 tr"} {
		for _, row := range d.Select(sel) {
			label := nodeText(row, ".a-text-bold")
			if label == "" {
				label = nodeText(row, "th")
			}
			if label == "" {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(row.TextSep(" ")), label))
			label = strings.Map(func(r rune) rune {
				if r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r == 0x200b {
					return -1
				}
				return r
			}, label)
			label = strings.TrimSpace(strings.Trim(label, " :：\t\n"))
			value = strings.TrimSpace(strings.TrimLeft(value, " :：\u200e\u200f"))
			switch strings.ToLower(label) {
			case "isbn-13", "isbn-10":
				if s := localmeta.ISBN(value); s != "" && (m["isbn"] == nil || len(s) == 13) {
					m["isbn"] = s
				}
			case "publisher":
				m["publisher"] = value
			case "publication date":
				m["pubdate"] = value
			case "language":
				m["language"] = value
			}
		}
	}
	m["comments"] = plainInternational(nodeText(d, "#bookDescription_feature_div .a-expander-content"))
	if img := d.SelectOne("#imgBlkFront"); img != nil {
		cover := img.Attr("data-old-hires")
		if cover == "" {
			cover = img.Attr("src")
		}
		m["cover_url"] = p.trustedCover(cover)
	}
	return m, nil
}

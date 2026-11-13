package httpapi

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"talebook/booksvr/internal/library"
	"testing"
	"time"
)

func TestOpdsHTTPAbsoluteLinksKeepHostPortAndPrefix(t *testing.T) {
	fx := newOpdsFixture(t)
	opdsSeedBook(t, fx.DB, 1, "Book", 1)
	opdsSeedFormat(t, fx.DB, 1, "EPUB")
	fx.Cfg.PublicURL = func(path string) string { return "/library" + path }
	for _, scheme := range []string{"http", "https"} {
		ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
		ctx.Request = httptest.NewRequest(http.MethodGet, "http://192.168.1.96:8850/opds/", nil)
		ctx.Request.Header.Set("X-Forwarded-Proto", scheme)
		ctx.Params = []string{opdsHexlify("Onewest")}
		if _, err := OpdsNavHandler(fx.Cfg)(ctx); err != nil {
			t.Fatal(err)
		}
		var f atomFeedXML
		if err := xml.Unmarshal([]byte(opdsBody(ctx)), &f); err != nil {
			t.Fatal(err)
		}
		prefix := scheme + "://192.168.1.96:8850/library/"
		if !strings.HasPrefix(f.Icon, prefix) {
			t.Fatal(f.Icon)
		}
		for _, link := range append(f.Links, f.Entries[0].Links...) {
			if !strings.HasPrefix(link.Href, prefix) {
				t.Fatal(link)
			}
		}
	}
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://192.168.1.96:8850/opds/", nil)
	fx.Cfg.PublicURL = func(path string) string { return "https://books.example/root" + path }
	cfg := opdsRequestConfig(fx.Cfg, ctx)
	if cfg.PublicURL("/opds/") != "https://books.example/root/opds/" {
		t.Fatal(cfg.PublicURL("/opds/"))
	}
	fx.Cfg.PublicURL = nil
	cfg = opdsRequestConfig(fx.Cfg, ctx)
	entry := opdsAcquisitionEntry(cfg, library.BookData{ID: 1, Formats: []string{"EPUB"}}, time.Now(), opdsPrefix(cfg))
	if entry.Links[0].Href != "http://192.168.1.96:8850/api/book/1.epub?from=opds" {
		t.Fatal(entry.Links[0])
	}
	valid := regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !valid.MatchString(entry.ID) || entry.ID != opdsBookURN(cfg, 1) || entry.ID == opdsBookURN(cfg, 2) {
		t.Fatal(entry.ID)
	}
}

func TestOpdsExplicitPaginationMetadata(t *testing.T) {
	fx := newOpdsFixture(t)
	for i := 1; i <= 65; i++ {
		opdsSeedBook(t, fx.DB, int64(i), fmt.Sprint("Book ", i), int64(i))
	}
	for _, offset := range []int{0, 30, 60} {
		ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
		body, err := opdsAllBooks(fx.Cfg, ctx, "newest", "/opds/nav/test", "/opds/", offset)
		if err != nil {
			t.Fatal(err)
		}
		var feed struct {
			Total int           `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
			Start int           `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex"`
			Size  int           `xml:"http://a9.com/-/spec/opensearch/1.1/ itemsPerPage"`
			Links []atomLinkXML `xml:"link"`
		}
		if err = xml.Unmarshal(body, &feed); err != nil {
			t.Fatal(err)
		}
		if feed.Total != 65 || feed.Start != offset+1 || feed.Size != 30 {
			t.Fatal(string(body))
		}
		hasNext, hasPrevious := false, false
		for _, link := range feed.Links {
			if link.Rel == "next" {
				hasNext = true
			}
			if link.Rel == "previous" {
				hasPrevious = true
			}
			if link.Rel == "next" || link.Rel == "previous" {
				if link.Type != opdsContentType {
					t.Fatal(link)
				}
			}
		}
		if hasNext != (offset < 60) || hasPrevious != (offset > 0) {
			t.Fatal(feed)
		}
	}
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
	root, err := opdsTopLevelFeed(fx.Cfg, ctx)
	if err != nil || strings.Contains(string(root), "opensearch:totalResults") {
		t.Fatal(string(root), err)
	}
}

func TestOpdsPageWidthBoundaries(t *testing.T) {
	for _, tc := range []struct{ pages, want int }{{1, 50}, {50, 50}, {51, 2500}, {2500, 2500}, {2501, 125000}, {8210, 125000}} {
		if got := opdsPageWidth(tc.pages); got != tc.want || !validOpdsPageWidth(got) {
			t.Fatal(tc, got)
		}
	}
	for _, bad := range []int{0, 1, 49, 51, 100, 2501, -50} {
		if validOpdsPageWidth(bad) {
			t.Fatal(bad)
		}
	}
}

func TestOpdsPageDirectoryCompleteBoundedCoverage(t *testing.T) {
	for _, pages := range []int{1, 50, 51, 2500, 2501} {
		t.Run(strconv.Itoa(pages), func(t *testing.T) {
			fx := newOpdsFixture(t)
			fx.Cfg.Settings["opds_max_items"] = 1
			tx, err := fx.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= pages; i++ {
				if _, err = tx.Exec(`INSERT INTO books(id,title,path,timestamp,series_index,has_cover) VALUES(?,?,?, ?,1,0)`, i, fmt.Sprint("Book ", i), "p", i); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			seen := map[int]bool{}
			var visit func(string, string)
			visit = func(query, parent string) {
				ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
				raw, err := opdsPageDirectory(fx.Cfg, ctx, query)
				if err != nil {
					t.Fatal(query, err)
				}
				var feed atomFeedXML
				if err = xml.Unmarshal(raw, &feed); err != nil {
					t.Fatal(err)
				}
				if len(feed.Entries) < 1 || len(feed.Entries) > 50 {
					t.Fatal(query, len(feed.Entries))
				}
				if parent != "" {
					up := ""
					for _, link := range feed.Links {
						if link.Rel == "up" {
							up = link.Href
						}
					}
					if up != parent {
						t.Fatal(query, up, parent)
					}
				}
				self := ""
				for _, link := range feed.Links {
					if link.Rel == "self" {
						self = link.Href
					}
					if link.Rel == "next" {
						t.Fatal("directory requires client pagination")
					}
				}
				for _, entry := range feed.Entries {
					link := entry.Links[0]
					if link.Rel != "subsection" {
						t.Fatal(link)
					}
					if strings.Contains(link.Type, "kind=navigation") {
						hex := strings.TrimPrefix(link.Href, "/opds/nav/")
						decoded, err := opdsUnhexlify(hex)
						if err != nil || !strings.HasPrefix(decoded, "P") {
							t.Fatal(link, err)
						}
						visit(decoded[1:], self)
					} else {
						if !strings.Contains(link.Type, "kind=acquisition") {
							t.Fatal(link)
						}
						parts := strings.Split(link.Href, "?offset=")
						offset, err := strconv.Atoi(parts[1])
						if err != nil || offset < 0 || offset >= pages || seen[offset] {
							t.Fatal(link, err)
						}
						seen[offset] = true
					}
				}
			}
			visit("newest", "")
			if len(seen) != pages {
				t.Fatal(len(seen), pages)
			}
		})
	}
}

func TestOpdsPageDirectoryVisibilityInvalidInputsAndRoot(t *testing.T) {
	fx := newOpdsFixture(t)
	fx.Cfg.Settings["opds_max_items"] = 2
	for i := 1; i <= 5; i++ {
		opdsSeedBook(t, fx.DB, int64(i), fmt.Sprint("Book ", i), int64(i))
	}
	createFormatTables(t, fx.DB)
	if _, err := fx.DB.Exec(`CREATE TABLE source_sync_formats(format_id INTEGER,missing INTEGER);CREATE TABLE book_unavailable_sources(book_id INTEGER PRIMARY KEY);INSERT INTO book_unavailable_sources VALUES(5)`); err != nil {
		t.Fatal(err)
	}
	fx.Cfg.ExcludedBookIDs = func(_ *Context) (map[int64]bool, error) { return map[int64]bool{4: true}, nil }
	ctx := pluginCtx(t, fx.DB, http.MethodGet, "/opds/", "", 0, false)
	for _, order := range []string{"newest", "title"} {
		raw, err := opdsPageDirectory(fx.Cfg, ctx, order)
		if err != nil {
			t.Fatal(err)
		}
		var feed atomFeedXML
		if err = xml.Unmarshal(raw, &feed); err != nil {
			t.Fatal(err)
		}
		if len(feed.Entries) != 2 || !strings.Contains(feed.Subtitle, "共 3 本") || !strings.HasSuffix(feed.Entries[1].Links[0].Href, "?offset=2") {
			t.Fatal(string(raw))
		}
	}
	for _, q := range []string{"unknown", "title:0:1", "title:1:50", "title:-50:50", "title:50:50", "title:0:2500", "title:x:50", "title:0:51", "title:0:50:0", "title:922337203685477580799:50"} {
		if _, err := opdsPageDirectory(fx.Cfg, ctx, q); err == nil {
			t.Fatal("invalid accepted", q)
		}
	}
	raw, err := opdsTopLevelFeed(fx.Cfg, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Pnewest", "Ptitle"} {
		if !strings.Contains(string(raw), opdsHexlify(q)) {
			t.Fatal("missing compatible entry", q)
		}
		body, e := opdsNavCatalog(fx.Cfg, ctx, opdsHexlify(q), 0)
		if e != nil || !strings.Contains(string(body), "按页浏览") {
			t.Fatal(string(body), e)
		}
	}
	// A legacy caller using a bulk permission callback receives the same count.
	fx.Cfg.ExcludedBookIDs = nil
	fx.Cfg.FilterVisibleBookIDs = func(_ *Context, ids []int64) ([]int64, error) { return []int64{1}, nil }
	raw, err = opdsPageDirectory(fx.Cfg, ctx, "newest")
	if err != nil || !strings.Contains(string(raw), "共 1 本") {
		t.Fatal(string(raw), err)
	}
	fx.DB.Exec(`DELETE FROM books`)
	fx.Cfg.FilterVisibleBookIDs = nil
	if _, err = opdsPageDirectory(fx.Cfg, ctx, "newest"); err != errOpdsNoBooks {
		t.Fatal(err)
	}
}

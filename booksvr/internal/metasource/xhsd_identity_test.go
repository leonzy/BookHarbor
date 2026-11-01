package metasource

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"talebook/booksvr/internal/safehttp"
)

func TestXhsdMissingISBNRoundTripsProductIdentity(t *testing.T) {
	const product = "https://item.xhsd.com/book/123"
	const isbn = "9787536692930"
	requests := 0
	src := NewXhsdSource(&safehttp.SafeHttpClient{
		Resolver: func(string, int) ([]string, error) { return []string{"93.184.216.34"}, nil },
		Session: &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
			requests++
			body := `<li class="product"><p class="product-desc"><a href="` + product + `">书籍</a></p></li>`
			if r.URL.Hostname() == "item.xhsd.com" {
				// A query term appearing in HTML is not proof of this edition's ISBN.
				body = `<!-- keyword=` + isbn + ` --><div class="item-title" data-item='{"name":"自由与毁灭","mainImage":"//img1.xinhuashudian.com/a.jpg","otherAttributes":[{"group":"BASIC","otherAttributes":[{"attrKey":"作者","attrVal":"彼得·麦克菲"},{"attrKey":"出版社","attrVal":"中信出版社"}]}]}'></div>`
			} else if r.URL.Hostname() != "search.xhsd.com" {
				t.Fatal("unexpected destination", r.URL)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})},
	})
	searched := src.Search(isbn, "自由与毁灭", false)
	if searched == nil || searched["provider_value"] != product || searched["isbn"] != "" {
		t.Fatal("search returned an unusable or false ISBN identity", searched)
	}
	fetched := src.Fetch(searched["provider_value"].(string), false)
	if fetched == nil || fetched["provider_value"] != product || fetched["isbn"] != "" || fetched["title"] != "自由与毁灭" {
		t.Fatal("selected product could not be fetched", fetched)
	}
	if got := src.Fetch(isbn, false); got == nil || got["provider_value"] != product {
		t.Fatal("legacy ISBN lookup broke", got)
	}
	before := requests
	for _, id := range []string{"https://evil.test/book/123", "http://item.xhsd.com/book/123", "https://item.xhsd.com.evil.test/123", "https://user:password@item.xhsd.com/123", "https://item.xhsd.com:8443/123", "https://item.xhsd.com/123#fragment", "https://item.xhsd.com/", "https://127.0.0.1/123", "0000000000002", "invalid"} {
		if got := src.Fetch(id, false); got != nil {
			t.Fatal("invalid identifier accepted", id, got)
		}
	}
	if requests != before {
		t.Fatal("invalid identifiers made HTTP requests")
	}
}

func TestXhsdIdentityUsesVerifiedISBNOrTrustedProduct(t *testing.T) {
	src := NewXhsdSource(nil)
	for _, tc := range []struct{ isbn, url, wantID, wantISBN string }{
		{"9787536692930", "https://evil.test/1", "9787536692930", "9787536692930"},
		{"978-7536-69293-0", "https://item.xhsd.com/1", "9787536692930", "9787536692930"},
		{"0000000000002", "https://item.xhsd.com:443/1", "https://item.xhsd.com/1", ""},
		{"", "https://item.xhsd.com/1", "https://item.xhsd.com/1", ""},
		{"", "https://evil.test/1", "", ""},
		{"", "", "", ""},
	} {
		got := src.BuildMetadata(&XhsdBookInfo{Title: "书籍", ISBN: tc.isbn, URL: tc.url}, false)
		if tc.wantID == "" {
			if got != nil {
				t.Fatal("unselectable candidate published", got)
			}
			continue
		}
		if got == nil || got["provider_value"] != tc.wantID || got["isbn"] != tc.wantISBN {
			t.Fatal(tc, got)
		}
	}
}

package pluginbuiltin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"talebook/booksvr/internal/plugindomain"
	"testing"
)

func TestInternationalGoogle(t *testing.T) {
	p := newInternationalProvider(false)
	calls := []string{}
	p.get = func(_ context.Context, u string) ([]byte, error) {
		calls = append(calls, u)
		return []byte(`{"id":"volume1","volumeInfo":{"title":"A book","authors":["Writer"],"industryIdentifiers":[{"type":"OTHER","identifier":"9780141439518"},{"type":"ISBN_13","identifier":"9780141439518"}],"imageLinks":{"thumbnail":"http://books.google.com/a.jpg"}}}`), nil
	}
	m, e := p.Fetch("volume1", nil)
	if e != nil || m["isbn"] != "9780141439518" || m["cover_url"] != "https://books.google.com/a.jpg" {
		t.Fatalf("%v %v", m, e)
	}
	if _, e = p.Fetch("http://127.0.0.1/a", nil); e == nil || len(calls) != 1 {
		t.Fatal("untrusted detail ID fetched")
	}
	var v googleVolume
	v.ID = "v"
	v.Info.Title = "t"
	v.Info.Identifiers = append(v.Info.Identifiers, struct {
		Type  string `json:"type"`
		Value string `json:"identifier"`
	}{"OTHER", "9780141439518"})
	if p.googleMap(v)["isbn"] != "" {
		t.Fatal("OTHER interpreted as ISBN")
	}
}
func TestInternationalGoogleAtom(t *testing.T) {
	p := newInternationalProvider(false)
	p.get = func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "googleapis") {
			return nil, fmt.Errorf("429")
		}
		return []byte(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms"><entry><id>https://books.google.com/books/feeds/volumes/abc</id><dc:title>Book</dc:title><dc:creator>Author</dc:creator><dc:identifier>ISBN:9780141439518</dc:identifier></entry></feed>`), nil
	}
	m, e := p.Search(plugindomain.MetadataQuery{Title: "Book"}, nil)
	if e != nil || len(m) != 1 || m[0]["isbn"] != "9780141439518" {
		t.Fatalf("%v %v", m, e)
	}
}
func TestInternationalAmazon(t *testing.T) {
	p := newInternationalProvider(true)
	p.get = func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "/s/?") {
			return []byte(`<div data-component-type="s-search-result" data-asin="0141439513"></div>`), nil
		}
		return []byte(`<span id="productTitle">Pride and Prejudice</span><div id="bylineInfo"><a class="a-link-normal">Jane Austen</a></div><div id="detailBullets_feature_div"><li><span class="a-text-bold">Publisher :</span> Penguin</li><li><span class="a-text-bold">ISBN-13 :</span> 978-0141439518</li><li><span class="a-text-bold">Language :</span> English</li></div><img id="imgBlkFront" src="https://m.media-amazon.com/a.jpg">`), nil
	}
	m, e := p.Search(plugindomain.MetadataQuery{ISBN: "9780141439518"}, nil)
	if e != nil || len(m) != 1 || m[0]["isbn"] != "9780141439518" || m[0]["publisher"] != "Penguin" {
		t.Fatalf("%v %v", m, e)
	}
	if p.trustedCover("https://m.media-amazon.com.evil.test/x") != "" {
		t.Fatal("evil cover trusted")
	}
	p.get = func(context.Context, string) ([]byte, error) { return []byte(`<input name="captchacharacters">`), nil }
	if _, e = p.Search(plugindomain.MetadataQuery{Title: "Book"}, nil); e == nil {
		t.Fatal("captcha swallowed")
	}
}
func TestInternationalLive(t *testing.T) {
	if os.Getenv("BOOKHARBOR_NATIVE_META_LIVE") != "1" {
		t.Skip("explicit live opt-in")
	}
	for _, amazon := range []bool{false, true} {
		p := newInternationalProvider(amazon)
		p.get = func(ctx context.Context, u string) ([]byte, error) {
			b, e := p.download(ctx, u)
			t.Logf("GET %s bytes=%d error=%v", u, len(b), e)
			return b, e
		}
		items, e := p.Search(plugindomain.MetadataQuery{ISBN: "9780141439518"}, nil)
		if e != nil || len(items) == 0 {
			t.Fatalf("%s search: %v items=%v", p.Manifest()["name"], e, items)
		}
		found := false
		for _, m := range items {
			t.Logf("%s candidate %v", p.Manifest()["name"], m)
			if m["isbn"] != "9780141439518" {
				continue
			}
			found = true
			detail, e := p.Fetch(m["provider_value"].(string), nil)
			if e != nil || detail["isbn"] != "9780141439518" {
				t.Fatalf("detail %v %v", detail, e)
			}
			cover, e := p.GetCover(detail["cover_url"].(string), nil)
			if e != nil || cover == nil || len(cover.Content) == 0 {
				t.Fatalf("cover %v", e)
			}
			t.Logf("%s cover bytes=%d", p.Manifest()["name"], len(cover.Content))
			break
		}
		if !found {
			t.Fatalf("exact ISBN absent from %v", items)
		}
	}
}

func TestInternationalAmazonVersionAndFailure(t *testing.T) {
	p := newInternationalProvider(true)
	calls := []string{}
	p.get = func(_ context.Context, u string) ([]byte, error) {
		calls = append(calls, u)
		if strings.Contains(u, "/s/?") {
			return []byte(`<div data-component-type="s-search-result" data-asin="B0DGG2RTWV"></div><div data-component-type="s-search-result" data-asin="0141439513"></div>`), nil
		}
		isbn := "9781998382347"
		if strings.HasSuffix(u, "0141439513") && len(calls) > 1 {
			isbn = "9780141439518"
		}
		return []byte(`<span id="productTitle">Pride and Prejudice</span><div id="detailBullets_feature_div"><li><span class="a-text-bold">ISBN-13 :</span>` + isbn + `</li></div>`), nil
	}
	rows, e := p.Search(plugindomain.MetadataQuery{ISBN: "9780141439518"}, nil)
	if e != nil || len(rows) != 2 || rows[0]["isbn"] != "9781998382347" || rows[1]["isbn"] != "9780141439518" {
		t.Fatalf("query ISBN forged or wrong edition hidden %v %v", rows, e)
	}
	if amazonISBNIdentifier("9780141439518") != "0141439513" || amazonISBNIdentifier("9791234567896") != "" {
		t.Fatal("ISBN hint invalid")
	}
	p.get = func(context.Context, string) ([]byte, error) {
		return []byte(`<html><title>Sorry</title>To discuss automated access to Amazon data please contact api-services-support@amazon.com</html>`), nil
	}
	if _, e = p.Search(plugindomain.MetadataQuery{Title: "Book"}, nil); e == nil {
		t.Fatal("blocked page treated as empty")
	}
}
func TestInternationalRejectsMalformedCatalogue(t *testing.T) {
	p := newInternationalProvider(false)
	p.get = func(context.Context, string) ([]byte, error) { return []byte(`<html>maintenance</html>`), nil }
	if _, e := p.Search(plugindomain.MetadataQuery{Title: "Book"}, nil); e == nil {
		t.Fatal("invalid XML accepted as empty feed")
	}
}

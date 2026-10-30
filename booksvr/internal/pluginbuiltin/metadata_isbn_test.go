package pluginbuiltin

import (
	"net/http"
	"strings"
	"talebook/booksvr/internal/plugindomain"
	"testing"
)

func TestOpenLibraryISBNUsesExactEdition(t *testing.T) {
	old := reviewHTTPClient
	defer func() { reviewHTTPClient = old }()
	calls := 0
	reviewHTTPClient = auditClient(func(r *http.Request) (int, string) {
		calls++
		if r.URL.Path != "/api/books" || r.URL.Query().Get("bibkeys") != "ISBN:9780141439518" || r.URL.Query().Get("jscmd") != "data" {
			t.Fatal(r.URL)
		}
		return 200, `{"ISBN:9780141439518":{"title":"Pride and Prejudice","url":"https://openlibrary.org/books/OL1M/Pride_and_Prejudice","authors":[{"name":"Jane Austen"}],"publishers":[{"name":"Penguin Classics"}],"publish_date":"2003","cover":{"large":"https://covers.openlibrary.org/b/id/1-L.jpg"}}}`
	})
	p := newOpenLibraryProvider()
	records, err := p.SearchBooks(plugindomain.MetadataQuery{Title: "不可靠的文件名", ISBN: "9780141439518"}, nil)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	r := records[0].ToDict()
	if r["isbn"] != "9780141439518" || r["publisher"] != "Penguin Classics" || r["provider_value"] != "/books/OL1M" || calls != 1 {
		t.Fatal(r, calls)
	}
}

func TestOpenLibrarySearchRequestsFieldsAndRejectsUUID(t *testing.T) {
	old := reviewHTTPClient
	defer func() { reviewHTTPClient = old }()
	reviewHTTPClient = auditClient(func(r *http.Request) (int, string) {
		if r.URL.Path != "/search.json" || !strings.Contains(r.URL.Query().Get("fields"), "isbn") {
			t.Fatal(r.URL)
		}
		return 200, `{"docs":[{"key":"/works/OL1W","title":"Pride and Prejudice","author_name":["Jane Austen"],"isbn":["uuid:0306406152","9780141439518"]}]}`
	})
	records, err := newOpenLibraryProvider().SearchBooks(plugindomain.MetadataQuery{Title: "Pride and Prejudice"}, nil)
	if err != nil || len(records) != 1 || records[0].ToDict()["isbn"] != "9780141439518" {
		t.Fatal(records, err)
	}
}

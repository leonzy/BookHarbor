package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestBookReadExplicitFormatDispatch(t *testing.T) {
	w := epubBookWiring(t, map[string]any{})
	for _, format := range []string{"PDF", "TXT", "MOBI", "AZW", "AZW3", "DJVU"} {
		if _, err := w.libraryDB.Exec(`INSERT INTO book_formats(book_id,format,path,size,mtime) VALUES(1,?,?,1,0)`, format, "/tmp/book."+strings.ToLower(format)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query    string
		status   int
		location string
	}{
		{"", http.StatusOK, ""},
		{"format=epub", http.StatusOK, ""},
		{"format=pdf", http.StatusFound, "/static/pdfjs/web/viewer.html?file=%2Fapi%2Fbook%2F1.PDF%3Finline%3D1"},
		{"format=PDF", http.StatusFound, "/static/pdfjs/web/viewer.html?file=%2Fapi%2Fbook%2F1.PDF%3Finline%3D1"},
		{"format=txt", http.StatusFound, "/book/1/readtxt"},
		{"format=mobi", http.StatusFound, "/readest/talebook-launch.html?bookId=1&format=mobi"},
		{"format=azw", http.StatusFound, "/readest/talebook-launch.html?bookId=1&format=azw"},
		{"format=azw3", http.StatusFound, "/readest/talebook-launch.html?bookId=1&format=azw3"},
		{"format=djvu", http.StatusUnsupportedMediaType, ""},
		{"format=missing", http.StatusUnsupportedMediaType, ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			r := serveRead(t, w, tc.query)
			if r.Code != tc.status || r.Header().Get("Location") != tc.location {
				t.Fatalf("status=%d location=%q body=%s", r.Code, r.Header().Get("Location"), r.Body.String())
			}
		})
	}
	if _, err := w.libraryDB.Exec(`DELETE FROM book_formats WHERE format='PDF'`); err != nil {
		t.Fatal(err)
	}
	if r := serveRead(t, w, "format=pdf"); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing PDF fell back to EPUB: %d", r.Code)
	}
}

func TestBookReadSinglePDFAndTXTHaveSameDefaultAndExplicitViewer(t *testing.T) {
	for _, format := range []string{"PDF", "TXT"} {
		t.Run(format, func(t *testing.T) {
			w := epubBookWiring(t, map[string]any{})
			if _, err := w.libraryDB.Exec(`UPDATE book_formats SET format=?`, format); err != nil {
				t.Fatal(err)
			}
			original, selected := serveRead(t, w, ""), serveRead(t, w, "format="+strings.ToLower(format))
			if selected.Code != http.StatusFound || original.Header().Get("Location") != selected.Header().Get("Location") {
				t.Fatalf("default=%d %q explicit=%d %q", original.Code, original.Header().Get("Location"), selected.Code, selected.Header().Get("Location"))
			}
		})
	}
}

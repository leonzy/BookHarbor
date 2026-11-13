package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSendDeviceSelectedFile(t *testing.T) {
	w, dir := newToolsFixture(t)
	for _, id := range []int64{1, 2} {
		insertBook(t, w.libraryDB, id, "书")
		insertItem(t, w.libraryDB, id, "public", 1)
		seedRealFormat(t, w, dir, id, "EPUB", "epub content")
	}
	azw3 := seedRealFormat(t, w, dir, 2, "AZW3", "selected azw3 content")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		calls++
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer req.MultipartForm.RemoveAll()
		f, h, err := req.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if string(data) != "selected azw3 content" || !strings.HasSuffix(h.Filename, ".azw3") {
			t.Errorf("wrong file %q %q", h.Filename, data)
		}
		rw.Write([]byte(`{"code":0,"successful":true}`))
	}))
	defer server.Close()
	args := map[string]any{"device_type": "boox", "device_url": server.URL, "format": " azw3 "}
	response, err := w.bookSendToDevice(adminCtx(1), 2, args)
	if err != nil || response["err"] != "ok" || response["format"] != "azw3" || calls != 1 {
		t.Fatalf("send: %v %v calls=%d", response, err, calls)
	}
	response, err = w.bookSendToDevice(adminCtx(1), 1, args)
	if err != nil || response["err"] != "format.not_found" || calls != 1 {
		t.Fatalf("fallback to another book/format: %v %v", response, err)
	}
	if err := os.Remove(azw3); err != nil {
		t.Fatal(err)
	}
	response, err = w.bookSendToDevice(adminCtx(1), 2, args)
	if err != nil || response["err"] != "file.missing" || calls != 1 {
		t.Fatalf("fallback on missing file: %v %v", response, err)
	}
}

func TestSendSelectedFormatPreservesVisibility(t *testing.T) {
	w, dir := newToolsFixture(t)
	insertBook(t, w.libraryDB, 1, "私藏")
	insertItem(t, w.libraryDB, 1, "private", 7)
	seedRealFormat(t, w, dir, 1, "EPUB", "epub")
	ctx := &Context{Identity: Identity{UserID: 0}}
	for _, send := range []func(*Context, int64, map[string]any) (map[string]any, error){w.bookSendToDevice, w.bookSendToMail} {
		response, err := send(ctx, 1, map[string]any{"device_type": "boox", "device_url": "http://127.0.0.1:1", "mailbox": "reader@example.com", "format": "EPUB"})
		if err != nil || response["err"] != "book.not_found" {
			t.Fatalf("private book leaked: %v %v", response, err)
		}
	}
}

func TestSendMailRejectsUnavailableSelectedFormat(t *testing.T) {
	w, dir := newToolsFixture(t)
	insertBook(t, w.libraryDB, 1, "书")
	insertItem(t, w.libraryDB, 1, "public", 1)
	seedRealFormat(t, w, dir, 1, "EPUB", "epub")
	response, err := w.bookSendToMail(adminCtx(1), 1, map[string]any{"mailbox": "reader@example.com", "format": "PDF"})
	if err != nil || response["err"] != "format.not_found" {
		t.Fatalf("unexpected fallback %v %v", response, err)
	}
}

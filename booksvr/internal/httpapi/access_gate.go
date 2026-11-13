package httpapi

import (
	"errors"
	"net/http"
	"time"
)

var errAccessResponseRevoked = errors.New("access response revoked")

func writeAccessUnavailable(w http.ResponseWriter) {
	w.Header().Del("Content-Length")
	w.Header().Del("ETag")
	w.Header().Del("Last-Modified")
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"err": "access.not_ready", "msg": "访问范围正在准备或已经变更，请刷新重试"})
}

// accessResponseWriter checks immediately before headers (including 304), and
// at most one second apart while a body is written. Previously transmitted
// bytes cannot be recalled. It intentionally does not implement ReaderFrom:
// sendfile must not bypass Write and its generation check.
type accessResponseWriter struct {
	http.ResponseWriter
	check         func() error
	headerWritten bool
	revoked       bool
	lastCheck     time.Time
}

func (w *accessResponseWriter) valid(force bool) bool {
	if w.revoked {
		return false
	}
	if !force && !w.lastCheck.IsZero() && time.Since(w.lastCheck) < time.Second {
		return true
	}
	w.lastCheck = time.Now()
	if w.check == nil || w.check() != nil {
		w.revoked = true
		return false
	}
	return true
}

func (w *accessResponseWriter) WriteHeader(code int) {
	if w.headerWritten {
		return
	}
	if !w.valid(true) {
		w.headerWritten = true
		writeAccessUnavailable(w.ResponseWriter)
		return
	}
	// Protected content must not be reusable after switching user identities.
	w.Header().Set("Cache-Control", "private, no-store")
	w.headerWritten = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessResponseWriter) Write(body []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	if !w.valid(false) {
		return 0, errAccessResponseRevoked
	}
	return w.ResponseWriter.Write(body)
}

func (w *accessResponseWriter) Flush() {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	if w.valid(false) {
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

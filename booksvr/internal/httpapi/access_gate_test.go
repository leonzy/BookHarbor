package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessResponseRejectsJSONAnd304AfterPermissionChange(t *testing.T) {
	for _, status := range []int{200, 304} {
		rr := httptest.NewRecorder()
		w := &accessResponseWriter{ResponseWriter: rr, check: func() error { return errors.New("changed") }}
		w.Header().Set("ETag", "secret-object")
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(status)
		if _, err := w.Write([]byte("SECRET BOOK TITLE")); err == nil {
			t.Fatal("stale body accepted")
		}
		if rr.Code != 503 || strings.Contains(rr.Body.String(), "SECRET") || rr.Header().Get("ETag") != "" || rr.Header().Get("Content-Length") != "" {
			t.Fatalf("stale response: %+v %s", rr, rr.Body.String())
		}
	}
}

func TestAccessStreamStopsOnNextGenerationCheck(t *testing.T) {
	rr := httptest.NewRecorder()
	revoked := false
	w := &accessResponseWriter{ResponseWriter: rr, check: func() error {
		if revoked {
			return errors.New("changed")
		}
		return nil
	}}
	if _, err := w.Write([]byte("already sent")); err != nil {
		t.Fatal(err)
	}
	revoked = true
	w.lastCheck = time.Now().Add(-2 * time.Second)
	if _, err := w.Write([]byte("must not send")); !errors.Is(err, errAccessResponseRevoked) {
		t.Fatal(err)
	}
	if rr.Body.String() != "already sent" {
		t.Fatal("stream leaked after check", rr.Body.String())
	}
}

func TestAccessServerMissingResolverResultDoesNotCallHandler(t *testing.T) {
	called := false
	s, err := NewServer([]RouteSpec{{Pattern: "/test", Methods: []string{"GET"}, Handler: func(*Context) (map[string]any, error) { called = true; return map[string]any{"err": "ok"}, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	s.PrepareAccess = func(*Context) (func() error, error) { return nil, nil }
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/test", nil))
	if called || rr.Code != 503 {
		t.Fatal("invalid scope fell through")
	}
}

func TestAccessServerChecksAgainAfterHandler(t *testing.T) {
	changed := false
	s, err := NewServer([]RouteSpec{{Pattern: "/test", Methods: []string{"GET"}, Handler: func(*Context) (map[string]any, error) { changed = true; return map[string]any{"title": "SECRET"}, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	s.PrepareAccess = func(*Context) (func() error, error) {
		return func() error {
			if changed {
				return errors.New("changed")
			}
			return nil
		}, nil
	}
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/test", nil))
	if rr.Code != 503 || strings.Contains(rr.Body.String(), "SECRET") {
		t.Fatal("stale JSON returned", rr.Body.String())
	}
}

package metasource

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"testing"

	"talebook/booksvr/internal/safehttp"
)

type coverTransport func(*http.Request) (*http.Response, error)

func (f coverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBaikeCoverUsesContentNotSuffix(t *testing.T) {
	webp, _ := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvCAADAAfQuQZVtf+BiOh/AAA=")
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 9, 13)), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		body       []byte
		status     int
		valid      bool
	}{
		{"webp-no-suffix", "/pic/abc?x-bce-process=image/format,f_auto", webp, 200, true},
		{"webp-jpg-suffix", "/pic/book.jpg", webp, 200, true},
		{"jpeg-png-suffix", "/pic/book.png", jpg.Bytes(), 200, true},
		{"png-no-suffix", "/pic/book", makePNG(t, 9, 13, color.White), 200, true},
		{"html", "/pic/book.jpg", []byte("<html>blocked</html>"), 200, false},
		{"truncated", "/pic/book", webp[:15], 200, false},
		{"failed-with-image", "/pic/book.jpg", jpg.Bytes(), 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := NewBaikeSource(&safehttp.SafeHttpClient{Session: &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() != BaikeCoverHost {
					t.Fatal("unexpected destination", r.URL)
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(tc.body)), Request: r}, nil
			})}})
			got := src.GetCover("https://"+BaikeCoverHost+tc.path, true)
			if !tc.valid {
				if got != nil {
					t.Fatal("invalid image accepted")
				}
				return
			}
			if got == nil || got.Extension != "jpg" {
				t.Fatal("valid artwork rejected")
			}
			im, err := jpeg.Decode(bytes.NewReader(got.Content))
			if err != nil || im.Bounds().Dx() != 9 || im.Bounds().Dy() != 13 {
				t.Fatal("bad JPEG", err)
			}
		})
	}
}

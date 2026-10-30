package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyCoverNormalizesWebPByContent(t *testing.T) {
	for _, encoded := range []string{
		"UklGRh4AAABXRUJQVlA4TBEAAAAvCAADAAfQuQZVtf+BiOh/AAA=",
		"UklGRjgAAABXRUJQVlA4ICwAAADwAQCdASoJAA0AAUAmJaACdLoB+AAETAAA/uveL/yU1/jblq/687nOmwAAAA==",
	} {
		data, _ := base64.StdEncoding.DecodeString(encoded)
		for _, typ := range []string{"image/webp", "image/png"} {
			ctx, cfg := filesFixture(t)
			cfg.AllowsImageProxyHost = func(string) bool { return true }
			cfg.FetchImage = func(context.Context, string) (int, http.Header, []byte, error) {
				return 200, http.Header{"Content-Type": []string{typ}}, data, nil
			}
			ctx.Request = httptest.NewRequest("GET", "/get/pcover?url="+urlQueryEscape("https://bkimg.cdn.bcebos.com/pic/no-extension?x-bce-process=image/format,f_auto"), nil)
			if _, err := ProxyImageHandler(cfg)(ctx); err != nil {
				t.Fatal(err)
			}
			r := ctx.Writer.(*httptest.ResponseRecorder)
			im, err := jpeg.Decode(bytes.NewReader(r.Body.Bytes()))
			if r.Code != 200 || r.Header().Get("Content-Type") != "image/jpeg" || err != nil {
				t.Fatal(r.Code, r.Header(), err)
			}
			if im.Bounds().Dx() != 9 || im.Bounds().Dy() != 13 {
				t.Fatal(im.Bounds())
			}
		}
	}
	ctx, cfg := filesFixture(t)
	cfg.AllowsImageProxyHost = func(string) bool { return true }
	cfg.FetchImage = func(context.Context, string) (int, http.Header, []byte, error) {
		return 200, http.Header{"Content-Type": []string{"image/webp"}}, []byte("broken"), nil
	}
	ctx.Request = httptest.NewRequest("GET", "/get/pcover?url=https://bkimg.cdn.bcebos.com/pic/abc", nil)
	ProxyImageHandler(cfg)(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != 502 {
		t.Fatal("bad image served")
	}
}

package pluginbuiltin

import (
	"errors"
	"talebook/booksvr/internal/pluginiface"
	"talebook/booksvr/internal/pluginproto"
	"testing"
)

func TestWereadMetadataDispatchUsesRealProtocolCoverType(t *testing.T) {
	p := newWereadProvider()
	var _ pluginiface.MetadataProvider = p
	for _, method := range []string{"search_books", "get_metadata"} {
		var args []any
		if method == "search_books" {
			args = []any{map[string]any{"title": "测试图书"}}
		} else {
			args = []any{"book-id"}
		}
		_, e := p.Invoke(method, args, map[string]any{})
		var upstream *pluginproto.UpstreamError
		if !errors.As(e, &upstream) || upstream.Code != "provider_unauthorized" {
			t.Fatal("metadata method failed before reaching its credential check", method, e)
		}
	}
	value, e := p.Invoke("get_cover", []any{"https://weread.qq.com/cover.jpg"}, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := value.(*pluginiface.CoverResult); !ok {
		t.Fatal("wrong protocol result type", value)
	}
}

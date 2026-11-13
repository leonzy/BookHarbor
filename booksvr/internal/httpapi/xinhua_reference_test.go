package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"talebook/booksvr/internal/metasource"
	"talebook/booksvr/internal/pluginbuiltin"
	"talebook/booksvr/internal/plugindomain"
	"talebook/booksvr/internal/pluginiface"
)

type xinhuaReferenceProvider struct {
	*referenceProvider
	metadata plugindomain.BookMetadata
}

func (p *xinhuaReferenceProvider) Invoke(method string, args []any, ctx map[string]any) (any, error) {
	return pluginbuiltin.DispatchInvoke(p, method, args, ctx)
}
func (p *xinhuaReferenceProvider) GetMetadata(id string, _ map[string]any) (*plugindomain.BookMetadata, error) {
	if id != "https://item.xhsd.com/book/123" {
		return nil, nil
	}
	return &p.metadata, nil
}

func TestBookReferenceAppliesXinhuaProductWithoutISBN(t *testing.T) {
	w, _, file := referenceFixture(t)
	if _, err := w.libraryDB.Exec(`UPDATE books SET title='自由与毁灭' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	values := metasource.NewXhsdSource(nil).BuildMetadata(&metasource.XhsdBookInfo{Title: "自由与毁灭", Author: "彼得·麦克菲", Publisher: "中信出版社", URL: "https://item.xhsd.com/book/123"}, false)
	p := &xinhuaReferenceProvider{referenceProvider: &referenceProvider{key: "talebook.meta.xhsd"}, metadata: plugindomain.NewBookMetadata(values)}
	if _, err := w.registry.Register(p, pluginiface.CapabilitySet{Interfaces: map[string]bool{"MetadataProvider": true}, HasSelfCheck: true}); err != nil {
		t.Fatal(err)
	}
	ctx := adminCtx(1)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/book/1/refer", nil)
	out, err := w.bookRefer(ctx, 1, map[string]any{"provider_key": p.key, "provider_value": values["provider_value"], "only_meta": true})
	if err != nil || out["err"] != "ok" {
		t.Fatal("selectable Xinhua candidate could not be applied", out, err)
	}
	b, err := bookGetOne(w.Config(""), nil, 1)
	if err != nil || b.ISBN != "" || b.Publisher != "中信出版社" || b.Authors[0] != "彼得·麦克菲" {
		t.Fatal("metadata missing or ISBN invented", b, err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "untouched original ebook" {
		t.Fatal("source book changed", err)
	}
}

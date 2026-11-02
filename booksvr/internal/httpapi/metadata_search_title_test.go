package httpapi

import (
	"net/http"
	"reflect"
	"talebook/booksvr/internal/library"
	"testing"
)

func TestMetadataSearchCleansPromotionsWithoutChangingBook(t *testing.T) {
	title := "我的北洋军旅生涯(历陈直系奉系军阀混战的幕后真相)(上层将领，生平传奇 亲身经历，一手史料)"
	book := library.BookData{Title: title, Authors: []string{"李藻麟"}, Publisher: "团结出版社"}
	original := book
	q := referenceQuery(&book)
	if q["title"] != "我的北洋军旅生涯" || !reflect.DeepEqual(q["authors"], book.Authors) || q["publisher"] != "团结出版社" {
		t.Fatal(q)
	}
	if !reflect.DeepEqual(book, original) {
		t.Fatal("stored book mutated")
	}
	good := map[string]any{"title": "我的北洋军旅生涯", "authors": []string{"李藻麟"}, "provider_value": "test-edition"}
	if !referenceRelevant(book, good) || matchingMetadata(book, []map[string]any{good}) == nil {
		t.Fatal("cleaned candidate rejected")
	}
	for _, bad := range []map[string]any{{"title": "我的北洋军旅生涯", "authors": []string{"其他作者"}}, {"title": "我的北洋军旅生涯（第2卷）", "authors": book.Authors}} {
		if matchingMetadata(book, []map[string]any{bad}) != nil {
			t.Fatal("wrong author or volume accepted", bad)
		}
	}
	book.ISBN = "9780141439518"
	good["isbn"] = "9787020091621"
	if matchingMetadata(book, []map[string]any{good}) != nil {
		t.Fatal("ISBN conflict ignored")
	}
	ctx := pluginCtx(t, nil, http.MethodPost, "/", `{"query":{"title":"`+title+`","authors":["李藻麟"],"isbn":"9780141439518"}}`, 1, true)
	cq, e := pluginBookQuery(ctx)
	if e != nil || cq["title"] != "我的北洋军旅生涯" || cq["isbn"] != "9780141439518" {
		t.Fatal(cq, e)
	}
}

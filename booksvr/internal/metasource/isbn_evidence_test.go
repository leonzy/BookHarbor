package metasource

import "testing"

func TestQueryISBNIsNeverCandidateEvidence(t *testing.T) {
	s := NewNeoDBSource(nil)
	m := s.BuildMetadata(NeoDBItem{Title: "Unrelated book"}, "9787020091621", false)
	if m["isbn"] != nil {
		t.Fatal("query ISBN echoed", m)
	}
	d := NewDoubanV2Source(nil)
	m = d.BuildMetadata(DoubanV2Item{Title: "No detail URL"}, "", "9787020091621", false)
	if m["isbn"] != nil {
		t.Fatal("query ISBN echoed", m)
	}
	info := ParseXhsdDetailHTML(`<div class="item-title" data-item='{"name":"Unrelated"}'></div>`, "https://item.xhsd.com/book", "9787020091621")
	if info == nil || info.ISBN != "" {
		t.Fatal(info)
	}
}

func TestDoubanMultipleCreatorsDoNotBecomePublisher(t *testing.T) {
	for _, abstract := range []string{"老舍 / 袁运生 / 人民文学出版社 / 2012-12 / 39.80元", "Jane Austen / Translator / Penguin Books / 2012 / 12.00"} {
		result := ParseDoubanV2Abstract(abstract)
		if result.Publisher == "袁运生" || result.Publisher == "Translator" || result.PubDate == "人民文学出版社" {
			t.Fatal(result)
		}
		if result.Publisher != "人民文学出版社" && result.Publisher != "Penguin Books" {
			t.Fatal(result)
		}
	}
}

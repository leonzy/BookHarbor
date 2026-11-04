package bookidentity

import "testing"

func TestISBNCanonical(t *testing.T) {
	for _, v := range []string{"0-306-40615-2", "9780306406157", "ISBN: ０３０６４０６１５２", "urn:isbn:978-0-306-40615-7"} {
		if ISBN(v) != "9780306406157" {
			t.Fatal(v, ISBN(v))
		}
	}
	for _, v := range []string{"0000000000", "9780306406158", "B0B3H9QFLF", "1234567890123"} {
		if ISBN(v) != "" {
			t.Fatal(v)
		}
	}
}
func TestIdentityEvidenceAndConflicts(t *testing.T) {
	base := Metadata{Title: "AI未来进行式", Authors: []string{"李开复", "陈楸帆"}, ISBN: "9787213101625", Publisher: "浙江人民出版社", Pubdate: "2022-05-01", Language: "zho"}
	cases := []struct {
		name          string
		m             Metadata
		work, edition bool
	}{
		{"promotion", Metadata{Title: "AI未来进行式（李开复陈楸帆新书，樊登、刘慈欣等强势推荐！全新预测AI时代未来趋势！） (财之道丛书)", Authors: []string{"李开复"}, ISBN: base.ISBN}, true, true},
		{"missing authors strong isbn", Metadata{Title: base.Title, ISBN: base.ISBN}, true, true},
		{"authors conflict", Metadata{Title: base.Title, Authors: []string{"张三"}, ISBN: base.ISBN}, false, false},
		{"partial cannot bridge", Metadata{Title: base.Title, Authors: []string{"李开复", "张三"}, ISBN: base.ISBN}, false, false},
		{"title conflict", Metadata{Title: "三体", Authors: base.Authors, ISBN: base.ISBN}, false, false},
		{"volume", Metadata{Title: base.Title + "上册", Authors: base.Authors, ISBN: base.ISBN}, true, false},
		{"revision", Metadata{Title: base.Title + "（修订版）", Authors: base.Authors, ISBN: base.ISBN}, true, false},
		{"language", Metadata{Title: base.Title, Authors: base.Authors, ISBN: base.ISBN, Language: "en"}, true, false},
		{"publisher", Metadata{Title: base.Title, Authors: base.Authors, ISBN: base.ISBN, Publisher: "另一出版社"}, true, false},
		{"publication", Metadata{Title: base.Title, Authors: base.Authors, ISBN: base.ISBN, Pubdate: "2024-01-01"}, true, false},
		{"different ISBN same work", Metadata{Title: base.Title, Authors: base.Authors, ISBN: "9780306406157"}, true, false},
		{"no isbn exact authors", Metadata{Title: base.Title, Authors: []string{"陈楸帆", "李开复"}}, true, true},
		{"no isbn partial authors", Metadata{Title: base.Title, Authors: []string{"李开复"}}, false, false},
		{"no isbn anonymous", Metadata{Title: base.Title, Authors: []string{"佚名"}}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Compare(base, c.m)
			if d.Work != c.work || d.Edition != c.edition {
				t.Fatalf("%+v", d)
			}
		})
	}
}
func TestTitleKeepsMeaningfulQualifiers(t *testing.T) {
	for _, s := range []string{"推荐系统（第二版）", "史记（上册）", "小说（一个女人的故事）", "全集", "数据科学（Python实践）"} {
		if CleanTitle(s) != s {
			t.Fatal(s, CleanTitle(s))
		}
	}
	if Compare(Metadata{Title: "史记上册", Authors: []string{"司马迁"}, ISBN: "9780306406157"}, Metadata{Title: "史记下册", Authors: []string{"司马迁"}, ISBN: "9780306406157"}).Edition {
		t.Fatal("volumes merged")
	}
}

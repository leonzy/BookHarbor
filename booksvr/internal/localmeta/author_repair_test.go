package localmeta

import "testing"

func TestEmptyPropertyDoesNotConsumeNextField(t *testing.T) {
	for _, text := range []string{
		"Title:           \nAuthor:          \nCreator:         www.allitebooks.com\nProducer: PDF4NET",
		"书名：\r\n作者：\r\n出版社：人民文学出版社\r\n",
		"Author:\nCreator: Adobe InDesign\nPublisher: Example",
	} {
		m := FromText(text)
		if len(m.Authors) != 0 || m.Title != "" {
			t.Fatalf("%+v", m)
		}
	}
	m := FromText("Author: Richard Dawkins\nCreator: Tool\nPublisher: Oxford")
	if len(m.Authors) != 1 || m.Authors[0] != "Richard Dawkins" || m.Publisher != "Oxford" {
		t.Fatal(m)
	}
	if !UnknownAuthor("Creator: www.allitebooks.com") || !UnknownAuthor("佚名, epubw.com") || UnknownAuthor("关河五十州, epubw.com") {
		t.Fatal("author evidence")
	}
}

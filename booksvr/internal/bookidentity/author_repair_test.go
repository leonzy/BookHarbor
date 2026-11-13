package bookidentity

import "testing"

func TestAuthorRepairIdentityAndEvidenceBoundaries(t *testing.T) {
	base := Metadata{Title: "雍正大传", Authors: []string{"关河五十州"}, ISBN: "9787514372793"}
	for _, authors := range [][]string{{"关河五十州, ePUBw.COM"}, {"ePUBw.COM"}, {"Creator: www.allitebooks.com"}} {
		other := base
		other.Authors = authors
		if d := Compare(base, other); !d.Work {
			t.Fatal(authors, d)
		}
		other.ISBN = ""
		if len(authors) == 1 && authors[0] != "关河五十州, ePUBw.COM" && Compare(base, other).Work {
			t.Fatal("site supplies identity")
		}
	}
	for _, c := range []struct {
		a, b []string
		isbn string
		want bool
	}{
		{[]string{"Richard Dawkins"}, []string{"Dawkins, Richard"}, "", true},
		{[]string{"Jason Fried", "David Heinemeier Hansson"}, []string{"Jason Fried / David Heinemeier Hansson"}, "", true},
		{[]string{"Steven D. Levitt"}, []string{"Steven Levitt"}, base.ISBN, true},
		{[]string{"Steven D. Levitt"}, []string{"Steven Levitt"}, "", false},
		{[]string{"John Smith"}, []string{"James Smith"}, base.ISBN, false},
		{[]string{"John Andrew Smith"}, []string{"John Brian Smith"}, base.ISBN, false},
		{[]string{"ePUBw.COM"}, []string{"SoBooks.cc"}, "", false},
	} {
		a, b := base, base
		a.Authors = c.a
		b.Authors = c.b
		a.ISBN = c.isbn
		b.ISBN = c.isbn
		if got := Compare(a, b); got.Work != c.want {
			t.Fatal(c, got)
		}
	}
}

func TestCompleteCoauthorWitnessAndSubtitleEvidence(t *testing.T) {
	base := Metadata{Title: "同书", ISBN: "9787514372793"}
	a, b, c := base, base, base
	a.Authors = []string{"Alice Smith"}
	b.Authors = []string{"Bob Jones"}
	c.Authors = []string{"Alice Smith", "Bob Jones"}
	if !GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("complete author witness rejected")
	}
	a.Authors = []string{"Alice Smith", "Carol White"}
	if GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("partial author bridge accepted")
	}
	a = base
	b = base
	a.Title = "登天的感觉"
	b.Title = "登天的感觉：我在哈佛大学做心理咨询"
	if !Compare(a, b).Work {
		t.Fatal("same ISBN missing subtitle rejected")
	}
	a.Title = "登天的感觉：心理咨询实用理论"
	if Compare(a, b).Work {
		t.Fatal("different subtitles erased")
	}
}

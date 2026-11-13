package bookidentity

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAnonymousSourceCompanionsRequireExactLocalEvidence(t *testing.T) {
	a := Metadata{Title: "大国重工", Authors: []string{"佚名"}, SourceFamilies: []string{"/books/目录/Q047-大国重工 齐橙"}}
	b := a
	if d := Compare(a, b); !d.Edition || d.Reason != "source_family" {
		t.Fatal(d)
	}
	for _, mode := range []string{"directory", "stem", "title", "isbn", "authors", "no-source", "network"} {
		x, y := a, a
		switch mode {
		case "directory":
			y.SourceFamilies = []string{"/elsewhere/目录/Q047-大国重工 齐橙"}
		case "stem":
			y.SourceFamilies = []string{"/books/目录/大国重工"}
		case "title":
			y.Title = "其他作品"
		case "isbn":
			x.ISBN, y.ISBN = "9787313191304", "9780306406157"
		case "authors":
			x.Authors, y.Authors = []string{"甲"}, []string{"乙"}
		case "no-source":
			y.SourceFamilies = nil
		case "network":
			data, _ := json.Marshal(x)
			y = Metadata{}
			if err := json.Unmarshal(data, &y); err != nil {
				t.Fatal(err)
			}
		}
		if Compare(x, y).Work {
			t.Fatal("unsafe source fallback", mode)
		}
	}
	b.Authors = []string{"明确作者"}
	if !Compare(a, b).Work {
		t.Fatal("one missing author should not reject companions")
	}
}

func TestAnonymousSourceSubsetDoesNotAbsorbNamesakes(t *testing.T) {
	a := Metadata{Title: "大国重工", SourceFamilies: []string{"/books/Q047-大国重工 齐橙"}}
	b, outsider := a, Metadata{Title: "大国重工", SourceFamilies: []string{"/other/大国重工"}}
	for root := 0; root < 2; root++ {
		if got := CompatibleSubset([]Metadata{a, b, outsider}, []int64{0, 0, 0}, root); !reflect.DeepEqual(got, []int{0, 1}) {
			t.Fatal(root, got)
		}
	}
	if got := CompatibleSubset([]Metadata{a, b, outsider}, []int64{0, 0, 0}, 2); len(got) != 0 {
		t.Fatal("namesake claimed", got)
	}
}

func TestSourceFamiliesCannotBridgeUnrelatedFileSets(t *testing.T) {
	a := Metadata{Title: "作品", SourceFamilies: []string{"/one/book"}}
	b := Metadata{Title: "作品", SourceFamilies: []string{"/one/book", "/two/book"}}
	c := Metadata{Title: "作品", SourceFamilies: []string{"/two/book"}}
	if GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("source paths formed a transitive bridge")
	}
	for root := 0; root < 3; root++ {
		if got := CompatibleSubset([]Metadata{a, b, c}, []int64{0, 0, 0}, root); len(got) != 0 {
			t.Fatal(root, got)
		}
	}
}

func TestKnownAuthorWitnessExplainsCompanionAndOtherEdition(t *testing.T) {
	a := Metadata{Title: "作品", SourceFamilies: []string{"/one/book"}}
	b := Metadata{Title: "作品", Authors: []string{"甲"}, SourceFamilies: []string{"/one/book"}}
	c := Metadata{Title: "作品", Authors: []string{"甲"}, SourceFamilies: []string{"/two/book"}}
	if !GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("real complete author witness discarded source companion")
	}
	outsider := Metadata{Title: "作品", Authors: []string{"乙"}, SourceFamilies: []string{"/other/book"}}
	for root := 0; root < 3; root++ {
		if got := CompatibleSubset([]Metadata{a, b, c, outsider}, []int64{0, 0, 0, 0}, root); !reflect.DeepEqual(got, []int{0, 1, 2}) {
			t.Fatal("source witness subset missed", root, got)
		}
	}
	c.Authors = []string{"乙"}
	if GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("source witness hid explicit author conflict")
	}
}

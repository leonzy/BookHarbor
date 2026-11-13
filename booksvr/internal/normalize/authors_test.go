package normalize

import (
	"reflect"
	"testing"
)

func TestCleanAuthorsSeparatorsSitesAndSurnameOrder(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want []string
	}{
		{"关河五十州, ePUBw.COM", []string{"关河五十州"}},
		{"Creator:         www.allitebooks.com", nil},
		{"reader@mail.example.com", nil},
		{"（www.sobooks.cc）", nil},
		{"Jason Fried / David Heinemeier Hansson", []string{"Jason Fried", "David Heinemeier Hansson"}},
		{"Dawkins, Richard", []string{"Richard Dawkins"}},
		{"Givens, Terryl L.", []string{"Terryl L. Givens"}},
		{"John Smith, James Brown", []string{"John Smith", "James Brown"}},
		{"李开复，陈楸帆", []string{"李开复", "陈楸帆"}},
		{"J. K. Rowling", []string{"J. K. Rowling"}},
		{"[美] 理查德·道金斯", []string{"[美] 理查德·道金斯"}},
		{"Kathy Walrath and Seth Ladd", []string{"Kathy Walrath", "Seth Ladd"}},
		{"Mitch Tulloch with the Windows Azure Team", []string{"Mitch Tulloch", "the Windows Azure Team"}},
		{"（俄）契诃夫著 汝龙译", []string{"（俄）契诃夫", "汝龙译"}},
		{"胡适 等", []string{"胡适"}},
		{"张三(www.sobooks.cc)", []string{"张三"}},
	} {
		t.Run(c.raw, func(t *testing.T) {
			if got := CleanAuthors([]string{c.raw}); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%q want %q", got, c.want)
			}
		})
	}
	if NormalizeAuthor("Dawkins, Richard") != NormalizeAuthor("Richard Dawkins") {
		t.Fatal("surname order")
	}
	if NormalizeAuthor("[美] 理查德·道金斯") != NormalizeAuthor("理查德·道金斯") {
		t.Fatal("country")
	}
}

func TestAuthorComparisonBilingualAndCountryEvidence(t *testing.T) {
	for _, pair := range [][2]string{
		{"【西班牙】塞万提斯", "塞万提斯"},
		{"[瑞典]斯蒂格·拉森", "斯蒂格·拉森(Stieg Larsson)"},
		{"斯蒂芬·金（Stephen King）", "斯蒂芬·金"},
		{"Özen Özkaya", "Ozen Ozkaya"},
	} {
		if NormalizeAuthor(pair[0]) != NormalizeAuthor(pair[1]) {
			t.Fatal(pair, NormalizeAuthor(pair[0]), NormalizeAuthor(pair[1]))
		}
	}
}

func TestStrongISBNLatinAliasesDoNotEraseNameConflicts(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Steven D. Levitt", "Steven Levitt", true},
		{"Terryl L. Givens", "Givens, Terryl L.", true},
		{"John Smith", "James Smith", false},
		{"John Andrew Smith", "John Brian Smith", false},
		{"J. Smith", "John Smith", false},
		{"John Andrew Smith", "John Smith", false},
	} {
		if got := LatinAuthorAlias(c.a, c.b); got != c.want {
			t.Fatal(c, got)
		}
	}
}

package bookidentity

import (
	"reflect"
	"sort"
	"testing"
)

func TestCompleteWitnessSupportsMissingISBNButNotMissingCoauthors(t *testing.T) {
	a := Metadata{Title: "从1到N：企业数字化生存指南", Authors: []string{"尤尔根·梅菲特", "沙莎"}, ISBN: "9787313191304"}
	b, c := a, a
	b.Authors = []string{"尤尔根·梅菲特"}
	c.ISBN = ""
	if !GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("full witness did not corroborate missing ISBN")
	}
	c.Authors = b.Authors
	if GroupCompatible([]Metadata{a, b, c}) {
		t.Fatal("missing ISBN and incomplete authors used as strong evidence")
	}
}

func TestReliableSubsetNeverAssignsAmbiguousPartialBridge(t *testing.T) {
	base := Metadata{Title: "作品", ISBN: "9787313191304"}
	a, b, c, d := base, base, base, base
	a.Authors = []string{"甲"}
	b.Authors = []string{"甲", "乙"}
	c.Authors = []string{"甲", "丙"}
	d = b
	d.ISBN = ""
	members := []Metadata{a, b, c, d}
	// Try every input permutation and initiating ID. The partial author must not
	// be claimed by whichever full record happened to arrive first.
	var permute func([]int, int)
	permute = func(order []int, pos int) {
		if pos < len(order) {
			for i := pos; i < len(order); i++ {
				order[i], order[pos] = order[pos], order[i]
				permute(order, pos+1)
				order[i], order[pos] = order[pos], order[i]
			}
			return
		}
		var values []Metadata
		for _, i := range order {
			values = append(values, members[i])
		}
		for root, id := range order {
			out := CompatibleSubset(values, make([]int64, 4), root)
			var actual []int
			for _, i := range out {
				actual = append(actual, order[i])
			}
			sort.Ints(actual)
			var want []int
			if id == 1 || id == 3 {
				want = []int{1, 3}
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatal(order, id, actual, want)
			}
		}
	}
	permute([]int{0, 1, 2, 3}, 0)
}

func TestBadManagedUnitDoesNotBlockReliableSeparatePair(t *testing.T) {
	base := Metadata{Title: "同题作品", Authors: []string{"甲", "乙"}, ISBN: "9787313191304"}
	good := base
	good.ISBN = ""
	partial, bad := base, base
	partial.Authors = []string{"甲"}
	bad.Authors = []string{"甲", "丙"}
	values := []Metadata{base, good, partial, bad}
	got := CompatibleSubset(values, []int64{0, 0, 7, 7}, 0)
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatal(got)
	}
	if got := CompatibleSubset(values, []int64{0, 0, 7, 7}, 2); len(got) != 0 {
		t.Fatal("invalid managed unit partly absorbed", got)
	}
}

func TestWeakNamesakeCannotVetoKnownISBNCompleteWitness(t *testing.T) {
	a := Metadata{Title: "同名作品", Authors: []string{"甲", "乙"}, ISBN: "9787313191304"}
	b, c, d := a, a, a
	b.Authors = []string{"甲"}
	c.Authors = []string{"甲"}
	c.ISBN = ""
	d.ISBN = ""
	values := []Metadata{a, b, c, d}
	for _, root := range []int{0, 1, 3} {
		if got := CompatibleSubset(values, make([]int64, 4), root); !reflect.DeepEqual(got, []int{0, 1, 3}) {
			t.Fatal(root, got)
		}
	}
	if got := CompatibleSubset(values, make([]int64, 4), 2); len(got) != 0 {
		t.Fatal("weak record forced into strong group", got)
	}
}

func TestPromotionPreservesContentSubtitlesAndVersions(t *testing.T) {
	base := "从1到N：企业数字化生存指南"
	if CleanTitle(base+"（来自麦肯锡团队的企业数字化指南）") != base {
		t.Fatal("promotion retained")
	}
	for _, suffix := range []string{"（来自未来的你）", "（来自研究团队的指南·修订版）", "（上册）", "（理论与实践）"} {
		if CleanTitle(base+suffix) == base {
			t.Fatal("meaningful qualifier removed", suffix)
		}
	}
}

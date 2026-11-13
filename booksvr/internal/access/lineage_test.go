package access

import "testing"

func TestAccessDerivedFormatRetainsSourceRangeAndRules(t *testing.T) {
	p := Policy{Principals: []Principal{{ID: "reader", Range: Ceiling{Mode: Selected, Targets: []Target{{Root: "source", Directory: "/public", Recursive: true}}}}}, Rules: []Rule{{ID: "private", Target: Target{Root: "source", Directory: "/secret", Recursive: true}, Mode: DenyAll}}}
	c := p.Compile("reader", false)
	parent := FileFacts{Root: "source", Directory: "/secret", Ready: true}
	child := FileFacts{Root: "source", Directory: "/public", Ready: true, Parents: []FileFacts{parent}}
	if c.Decide(child).Allowed {
		t.Fatal("derived output widened original permission")
	}
	child.Parents[0].Directory = "/public"
	if !c.Decide(child).Allowed {
		t.Fatal("authorized derivation denied")
	}
	child.Parents[0].Inherited = []string{"missing-origin"}
	if c.Decide(child).Allowed {
		t.Fatal("unknown inherited rule accepted")
	}
	if !p.Compile("admin", true).Decide(child).Allowed {
		t.Fatal("administrator resource bypass lost")
	}
}

func TestAccessLineageDepthFailsClosed(t *testing.T) {
	f := FileFacts{Root: "root", Directory: "/", Ready: true}
	for i := 0; i < 18; i++ {
		f = FileFacts{Root: "root", Directory: "/", Ready: true, Parents: []FileFacts{f}}
	}
	if (Policy{}).Compile("reader", false).Decide(f).Allowed {
		t.Fatal("unbounded lineage accepted")
	}
}

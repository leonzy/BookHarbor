package access

import "testing"

func TestExplicitOriginReleasePreservesCurrentAndPrivateRestrictions(t *testing.T) {
	old := Rule{ID: "origin:1:1:old", Target: Target{Root: "root", Directory: "/old", Recursive: true}, Mode: DenyAll}
	p := Policy{OriginRules: []Rule{old}}
	f := FileFacts{Ready: true, Root: "root", Directory: "/new", Inherited: []string{old.ID}}
	if p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("historical restriction lost")
	}
	p.ReleasedOrigins = []string{old.ID}
	if err := p.Validate(map[string]bool{"reader": true}); err != nil {
		t.Fatal(err)
	}
	if !p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("explicit release ignored")
	}
	f.Private = true
	f.PrivateOwner = "other"
	if p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("release removed private protection")
	}
	f.Private = false
	p.Rules = []Rule{{ID: "current", Target: Target{Root: "root", Directory: "/new"}, Mode: DenyAll}}
	if p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("release removed current directory restriction")
	}
	p.ReleasedOrigins = []string{"unknown"}
	if p.Validate(map[string]bool{"reader": true}) == nil {
		t.Fatal("unknown origin released")
	}
}

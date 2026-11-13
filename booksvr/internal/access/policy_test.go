package access

import "testing"

func TestUninitializedPolicyAndMutableDraft(t *testing.T) {
	var empty Compiled
	if empty.Unrestricted() || empty.Decide(facts("/public")).Allowed {
		t.Fatal("uninitialized policy permits access")
	}
	p := Policy{Principals: []Principal{{ID: "a", Range: selected(directory("/public"))}}}
	c := p.Compile("a", false)
	p.Principals[0].Range.Targets[0].Directory = "/secret"
	if !c.Decide(facts("/public")).Allowed || c.Decide(facts("/secret")).Allowed {
		t.Fatal("draft mutation changed compiled permissions")
	}
}

func directory(name string) Target {
	return Target{Root: "root-books", Directory: name, Recursive: true}
}
func selected(targets ...Target) Ceiling { return Ceiling{Mode: Selected, Targets: targets} }
func facts(dir string) FileFacts         { return FileFacts{Root: "root-books", Directory: dir, Ready: true} }

func TestCeilingsIntersectAndTargetsUnion(t *testing.T) {
	p := Policy{Groups: []Group{
		{ID: "child", Name: "少儿", Members: []string{"reader"}, Range: selected(directory("/children"), directory("/science"))},
		{ID: "family", Name: "家人", Members: []string{"reader"}, Range: selected(directory("/children"))},
		{ID: "public", Name: "普通成员", Members: []string{"reader"}, Range: Ceiling{Mode: Unrestricted}},
	}}
	if err := p.Validate(map[string]bool{"reader": true}); err != nil {
		t.Fatal(err)
	}
	c := p.Compile("reader", false)
	for _, tt := range []struct {
		dir  string
		want bool
	}{{"/children", true}, {"/children/a", true}, {"/science", false}, {"/children-other", false}, {"/new-public", false}} {
		if got := c.Decide(facts(tt.dir)); got.Allowed != tt.want {
			t.Errorf("%s: %+v", tt.dir, got)
		}
	}
	if c.Unrestricted() {
		t.Fatal("ceiling classified as unrestricted")
	}
}

func TestAncestorRestrictionsAndPrivateOwnership(t *testing.T) {
	p := Policy{Groups: []Group{{ID: "family", Name: "家人", Members: []string{"owner"}, Range: Ceiling{Mode: Unrestricted}}}, Rules: []Rule{
		{ID: "parent", Target: directory("/children"), Mode: Selected, Groups: []string{"family"}},
		{ID: "child", Target: directory("/children/internal"), Mode: Selected, Users: []string{"other"}},
	}}
	if err := p.Validate(map[string]bool{"owner": true, "other": true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "other"} {
		if p.Compile(id, false).Decide(facts("/children/internal")).Allowed {
			t.Fatalf("%s bypassed ancestor intersection", id)
		}
	}
	f := facts("/children")
	f.Private = true
	f.PrivateOwner = "other"
	if p.Compile("owner", false).Decide(f).Allowed {
		t.Fatal("directory permission bypassed private")
	}
	if p.Compile("other", false).Decide(f).Allowed {
		t.Fatal("ownership bypassed directory")
	}
	if !p.Compile("admin", true).Decide(f).Allowed {
		t.Fatal("admin unable to preview")
	}
	f.Ready = false
	if p.Compile("admin", true).Decide(f).Allowed {
		t.Fatal("admin bypassed unavailable source")
	}
}

func TestMixedFormatsUseActualFileAndRoot(t *testing.T) {
	p := Policy{Principals: []Principal{{ID: "reader", Range: selected(directory("/public"))}}}
	c := p.Compile("reader", false)
	if !c.Decide(facts("/public")).Allowed || c.Decide(facts("/secret")).Allowed {
		t.Fatal("mixed formats incorrectly authorized")
	}
	f := facts("/public")
	f.Root = "replacement-mount"
	if c.Decide(f).Allowed {
		t.Fatal("unrecognized mount became public")
	}
	for _, dir := range []string{"/public/../secret", "/public\\secret", "/public\x00"} {
		if c.Decide(facts(dir)).Allowed {
			t.Fatalf("invalid path accepted %q", dir)
		}
	}
}

func TestProfileKeysIncludeOriginsAndRange(t *testing.T) {
	p := Policy{Groups: []Group{
		{ID: "g1", Name: "组一", Members: []string{"a"}, Range: selected(directory("/public"))},
		{ID: "g2", Name: "组二", Members: []string{"b"}, Range: selected(directory("/public"))},
	}}
	if p.Compile("a", false).Key() != p.Compile("b", false).Key() {
		t.Fatal("equivalent groups do not share")
	}
	p.OriginRules = []Rule{{ID: "original-secret-v1", Target: directory("/secret"), Mode: Selected, Users: []string{"a"}}}
	if p.Compile("a", false).Key() == p.Compile("b", false).Key() {
		t.Fatal("origins missing in profile key")
	}
	f := facts("/public")
	f.Inherited = []string{"original-secret-v1"}
	if !p.Compile("a", false).Decide(f).Allowed || p.Compile("b", false).Decide(f).Allowed {
		t.Fatal("move lost source restrictions")
	}
	f.Inherited = []string{"missing-origin"}
	if p.Compile("a", false).Decide(f).Allowed {
		t.Fatal("unknown origin allowed")
	}
	p.OriginRules = nil
	p.Groups[1].Range = selected(directory("/different"))
	if p.Compile("a", false).Key() == p.Compile("b", false).Key() {
		t.Fatal("ceiling missing in profile key")
	}
}

func TestValidationRejectsAmbiguousOrDanglingRules(t *testing.T) {
	users := map[string]bool{"reader": true}
	for _, p := range []Policy{
		{Principals: []Principal{{ID: "reader", Range: Ceiling{Mode: Selected}}}},
		{Principals: []Principal{{ID: "reader", Range: Ceiling{}}}},
		{Principals: []Principal{{ID: "reader", Range: Ceiling{Mode: Unrestricted, Targets: []Target{directory("/")}}}}},
		{Rules: []Rule{{ID: "r", Target: directory("/"), Mode: Selected}}},
		{Rules: []Rule{{ID: "r", Target: directory("/"), Mode: Selected, Groups: []string{"deleted"}}}},
		{Rules: []Rule{{ID: "r", Target: directory("/"), Mode: Selected, Users: []string{"deleted"}}}},
		{Principals: []Principal{{ID: "reader", Range: selected(Target{Class: "ordinary-tag"})}}},
		{Principals: []Principal{{ID: "reader", Range: selected(Target{Root: "root", Directory: "/x/../secret"})}}},
	} {
		if p.Validate(users) == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
	p := Policy{Principals: []Principal{{ID: "reader", Range: Ceiling{Mode: DenyAll}}}}
	if err := p.Validate(users); err != nil {
		t.Fatal(err)
	}
	if p.Compile("reader", false).Decide(facts("/public")).Allowed {
		t.Fatal("deny-all treated as unrestricted")
	}
}

func TestPermissionClassNeedsExplicitTrustedAssignment(t *testing.T) {
	p := Policy{Classes: []Class{{ID: "science", Name: "科普"}}, Principals: []Principal{{ID: "reader", Range: selected(Target{Class: "science"})}}}
	if err := p.Validate(map[string]bool{"reader": true}); err != nil {
		t.Fatal(err)
	}
	f := facts("/new")
	if p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("unclassified book allowed")
	}
	f.Classes = []string{"science"}
	if !p.Compile("reader", false).Decide(f).Allowed {
		t.Fatal("trusted class not applied")
	}
}

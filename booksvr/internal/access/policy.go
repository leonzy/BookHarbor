// Package access compiles library visibility rules. It does not access the
// filesystem or authorize an HTTP operation: callers must also enforce account
// capabilities, source availability and the actual file selected for serving.
package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type Mode string

const (
	Unrestricted Mode = "unrestricted"
	Selected     Mode = "selected"
	DenyAll      Mode = "deny_all"
)

// Target binds to an ACL-owned source identity, not a device number or a
// rebuildable directory node ID. Directory is relative to that logical root.
type Target struct {
	Root      string `json:"root,omitempty"`
	Directory string `json:"directory,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Class     string `json:"class,omitempty"`
}

type Ceiling struct {
	Mode    Mode     `json:"mode"`
	Targets []Target `json:"targets"`
}

type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Range   Ceiling  `json:"range"`
}

type Principal struct {
	ID    string  `json:"id"`
	Range Ceiling `json:"range"`
}

type Rule struct {
	ID     string   `json:"id"`
	Target Target   `json:"target"`
	Mode   Mode     `json:"mode"`
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

type Class struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Policy struct {
	Groups          []Group     `json:"groups"`
	Principals      []Principal `json:"principals"`
	Rules           []Rule      `json:"rules"`
	Classes         []Class     `json:"classes"`
	OriginRules     []Rule      `json:"origin_rules"`
	ReleasedOrigins []string    `json:"released_origins,omitempty"`
}

// WithRevisionOrigins assigns immutable identities to this revision's resource
// restrictions. A move can retain those identities without retaining a path.
func (p Policy) WithRevisionOrigins(revision, version int64) Policy {
	p.OriginRules = append([]Rule(nil), p.OriginRules...)
	seen := map[string]bool{}
	for _, r := range p.OriginRules {
		seen[r.ID] = true
	}
	for _, r := range p.Rules {
		r.ID = fmt.Sprintf("origin:%d:%d:%s", revision, version, r.ID)
		if !seen[r.ID] {
			p.OriginRules = append(p.OriginRules, r)
		}
	}
	return p
}

func (p Policy) MatchingOriginRules(revision, version int64, f FileFacts) []string {
	ids := []string{}
	for _, r := range p.Rules {
		if r.Target.matches(f) {
			ids = append(ids, fmt.Sprintf("origin:%d:%d:%s", revision, version, r.ID))
		}
	}
	return ids
}

// FileFacts comes only from trusted, versioned source and permission indexes.
// Inherited stores immutable source restrictions when a file moves. It must
// never be reconstructed from arbitrary metadata tags supplied by readers.
type FileFacts struct {
	Root         string
	Directory    string
	Classes      []string
	Inherited    []string
	PrivateOwner string
	Private      bool
	Ready        bool
	// Parents retain the source authority of generated formats. Moving output
	// into the managed library must not expand the source's permitted audience.
	Parents []FileFacts
}

type Decision struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons"`
}

func validID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func validDirectory(dir string) bool {
	return len(dir) <= 4096 && strings.HasPrefix(dir, "/") && path.Clean(dir) == dir && !strings.ContainsAny(dir, "\x00\\")
}

func (t Target) validate(classes map[string]bool) error {
	if t.Class != "" {
		if !classes[t.Class] || t.Root != "" || t.Directory != "" || t.Recursive {
			return fmt.Errorf("权限分类无效或与目录混用")
		}
		return nil
	}
	if !validID(t.Root) || !validDirectory(t.Directory) {
		return fmt.Errorf("目录必须使用稳定来源标识和规范路径")
	}
	return nil
}

func (c Ceiling) validate(classes map[string]bool) error {
	if c.Mode != Unrestricted && c.Mode != Selected && c.Mode != DenyAll {
		return fmt.Errorf("必须明确选择可浏览范围模式")
	}
	if (c.Mode == Selected) != (len(c.Targets) > 0) || len(c.Targets) > 256 {
		return fmt.Errorf("仅选定范围必须包含目标；其他模式不能携带目标")
	}
	for _, target := range c.Targets {
		if err := target.validate(classes); err != nil {
			return err
		}
	}
	return nil
}

// Validate rejects ambiguity rather than silently making a rule public.
// principals contains the stable IDs of known accounts, including the guest
// principal. Deletion of a referenced group/account is therefore rejected.
func (p Policy) Validate(principals map[string]bool) error {
	if len(p.Groups) > 256 || len(p.Rules)+len(p.OriginRules) > 4096 || len(p.Principals) > 10000 || len(p.Classes) > 1024 {
		return fmt.Errorf("权限规则数量超过准备预算")
	}
	classes, groups, users, rules := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range p.Classes {
		if !validID(c.ID) || classes[c.ID] || strings.TrimSpace(c.Name) == "" || len(c.Name) > 256 {
			return fmt.Errorf("权限分类标识或名称无效")
		}
		classes[c.ID] = true
	}
	for _, g := range p.Groups {
		if !validID(g.ID) || groups[g.ID] || strings.TrimSpace(g.Name) == "" || len(g.Name) > 256 {
			return fmt.Errorf("用户组标识或名称无效")
		}
		groups[g.ID] = true
		if err := g.Range.validate(classes); err != nil {
			return fmt.Errorf("用户组 %s: %w", g.ID, err)
		}
		seen := map[string]bool{}
		for _, id := range g.Members {
			if !principals[id] || seen[id] {
				return fmt.Errorf("用户组包含未知或重复账号")
			}
			seen[id] = true
		}
	}
	for _, u := range p.Principals {
		if !principals[u.ID] || users[u.ID] {
			return fmt.Errorf("未知或重复账号范围")
		}
		users[u.ID] = true
		if err := u.Range.validate(classes); err != nil {
			return err
		}
	}
	for index, r := range append(append([]Rule{}, p.Rules...), p.OriginRules...) {
		historical := index >= len(p.Rules)
		if !validID(r.ID) || rules[r.ID] {
			return fmt.Errorf("重复或无效规则标识")
		}
		rules[r.ID] = true
		targetClasses := classes
		if historical && r.Target.Class != "" {
			targetClasses = map[string]bool{r.Target.Class: true}
		}
		if err := r.Target.validate(targetClasses); err != nil {
			return err
		}
		if r.Mode != Selected && r.Mode != DenyAll {
			return fmt.Errorf("资源规则必须明确限制；无新增限制应移除该规则")
		}
		if (r.Mode == Selected) != (len(r.Users)+len(r.Groups) > 0) {
			return fmt.Errorf("允许名单不能为空；仅管理员模式不能携带名单")
		}
		for _, id := range r.Users {
			if !historical && !principals[id] {
				return fmt.Errorf("规则引用未知账号")
			}
		}
		for _, id := range r.Groups {
			if !historical && !groups[id] {
				return fmt.Errorf("规则引用未知用户组")
			}
		}
	}
	knownOrigins := map[string]bool{}
	for _, rule := range p.OriginRules {
		knownOrigins[rule.ID] = true
	}
	released := map[string]bool{}
	for _, id := range p.ReleasedOrigins {
		if !knownOrigins[id] || released[id] {
			return fmt.Errorf("解除继承必须选择已知且不重复的历史来源规则")
		}
		released[id] = true
	}
	return nil
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func (t Target) matches(f FileFacts) bool {
	if t.Class != "" {
		return contains(f.Classes, t.Class)
	}
	if t.Root == "" || t.Root != f.Root || !validDirectory(f.Directory) {
		return false
	}
	return f.Directory == t.Directory || (t.Recursive && strings.HasPrefix(f.Directory, strings.TrimSuffix(t.Directory, "/")+"/"))
}

type Compiled struct {
	principal string
	admin     bool
	groups    map[string]bool
	ceilings  []Ceiling
	rules     []Rule
	origins   map[string]bool
	key       string
	valid     bool
}

// Compile is performed on policy changes, never per book in a browse request.
// Equivalent permission policies share a key even if membership names differ.
// Private ownership is intentionally excluded: it is a sparse per-user delta.
func (p Policy) Compile(principal string, admin bool) Compiled {
	c := Compiled{principal: principal, admin: admin, groups: map[string]bool{}, origins: map[string]bool{}, valid: true}
	for _, g := range p.Groups {
		if contains(g.Members, principal) {
			c.groups[g.ID] = true
			if g.Range.Mode != Unrestricted {
				c.ceilings = append(c.ceilings, copyCeiling(g.Range))
			}
		}
	}
	for _, u := range p.Principals {
		if u.ID == principal && u.Range.Mode != Unrestricted {
			c.ceilings = append(c.ceilings, copyCeiling(u.Range))
		}
	}
	for _, r := range p.Rules {
		if !c.permits(r) {
			c.rules = append(c.rules, r)
		}
	}
	keys := []string{}
	for _, r := range p.OriginRules {
		c.origins[r.ID] = c.permits(r) || contains(p.ReleasedOrigins, r.ID)
		keys = append(keys, fmt.Sprintf("origin:%s:%t", r.ID, c.origins[r.ID]))
	}
	for _, ceiling := range c.ceilings {
		targets := make([]string, 0, len(ceiling.Targets))
		for _, target := range ceiling.Targets {
			b, _ := json.Marshal(target)
			targets = append(targets, string(b))
		}
		sort.Strings(targets)
		b, _ := json.Marshal([]any{ceiling.Mode, unique(targets)})
		keys = append(keys, string(b))
	}
	for _, r := range c.rules {
		b, _ := json.Marshal(r.Target)
		keys = append(keys, "deny:"+string(b))
	}
	sort.Strings(keys)
	if admin {
		keys = []string{"admin"}
	}
	b, _ := json.Marshal(unique(keys))
	h := sha256.Sum256(b)
	c.key = hex.EncodeToString(h[:])
	return c
}

func unique(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if len(out) == 0 || out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}

func (c Compiled) Key() string { return c.key }

func copyCeiling(c Ceiling) Ceiling {
	c.Targets = append([]Target(nil), c.Targets...)
	return c
}

// Unrestricted only describes current directory/class rules. Private/source
// checks and historical inherited restrictions remain mandatory separately.
func (c Compiled) Unrestricted() bool {
	if !c.valid {
		return false
	}
	if c.admin {
		return true
	}
	if len(c.ceilings) > 0 || len(c.rules) > 0 {
		return false
	}
	for _, allowed := range c.origins {
		if !allowed {
			return false
		}
	}
	return true
}

func (c Compiled) permits(r Rule) bool {
	if r.Mode != Selected {
		return false
	}
	if contains(r.Users, c.principal) {
		return true
	}
	for _, id := range r.Groups {
		if c.groups[id] {
			return true
		}
	}
	return false
}

func (c Compiled) Decide(f FileFacts) Decision {
	return c.decide(f, 0)
}

func (c Compiled) decide(f FileFacts, depth int) Decision {
	if depth > 16 || len(f.Parents) > 32 {
		return Decision{Reasons: []string{"origin.invalid"}}
	}
	d := Decision{Reasons: []string{}}
	if !c.valid {
		return Decision{Reasons: []string{"policy.not_ready"}}
	}
	if !f.Ready {
		d.Reasons = append(d.Reasons, "source.not_ready")
	}
	if !c.admin {
		for _, parent := range f.Parents {
			if !c.decide(parent, depth+1).Allowed {
				d.Reasons = append(d.Reasons, "origin.restricted")
				break
			}
		}
		for _, ceiling := range c.ceilings {
			match := false
			if ceiling.Mode == Selected {
				for _, target := range ceiling.Targets {
					if target.matches(f) {
						match = true
						break
					}
				}
			}
			if !match {
				d.Reasons = append(d.Reasons, "principal.out_of_range")
				break
			}
		}
		for _, rule := range c.rules {
			if rule.Target.matches(f) {
				d.Reasons = append(d.Reasons, "rule:"+rule.ID)
			}
		}
		// A move's inherited constraints apply regardless of its new path.
		for _, id := range f.Inherited {
			if !c.origins[id] {
				d.Reasons = append(d.Reasons, "origin:"+id)
			}
		}
		if f.Private && (f.PrivateOwner == "" || c.principal != f.PrivateOwner) {
			d.Reasons = append(d.Reasons, "book.private")
		}
	}
	d.Allowed = len(d.Reasons) == 0
	return d
}

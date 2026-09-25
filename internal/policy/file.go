package policy

import (
	"fmt"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

// FileVersion is the policy file format.
const FileVersion = 1

// File is one policy file, or the merge of several.
//
//	version: 1
//	baseline:
//	  by_effect:
//	    write: { human: approve, agent: approve }
//	providers:
//	  kubernetes:
//	    rules:
//	      - { ops: [delete_pod], decision: approve, approval: { channel: out_of_band } }
//	targets:
//	  - match: { owner: "!self" }
//	    rules:
//	      - { effect: [write, lifecycle, destructive, exec], decision: deny, reason: "Not ours to change." }
//	principals:
//	  - match: { kind: agent, client: "claude-code*" }
//	    rules:
//	      - { effect: [read_sensitive], decision: approve }
type File struct {
	Version int `yaml:"version"`
	// Posture is secure, the default, or permissive (section 13, Decision
	// 13, settled at the P2-5 review). The global posture governs the
	// host-wide switches, which have no target, and is where target-scoped
	// evaluation starts; posture_rules adjust only target-scoped
	// evaluation (PostureFor). Both are part of the snapshot hash and
	// change only through `cerberus policy apply`.
	Posture      string        `yaml:"posture,omitempty"`
	PostureRules []PostureRule `yaml:"posture_rules,omitempty"`

	Baseline   *BaselineOverride   `yaml:"baseline,omitempty"`
	Providers  map[string]Provider `yaml:"providers,omitempty"`
	Targets    []TargetBlock       `yaml:"targets,omitempty"`
	Principals []PrincipalBlock    `yaml:"principals,omitempty"`
}

// Postures.
const (
	PostureSecure     = "secure"
	PosturePermissive = "permissive"
)

// PostureRule sets the posture for the targets it matches (P2-5), for
// target-scoped evaluation only: it never changes a host-wide switch.
type PostureRule struct {
	Match   TargetMatch `yaml:"match"`
	Posture string      `yaml:"posture"`
}

// GlobalPosture is the file's global posture: secure unless it says
// permissive.
func (f File) GlobalPosture() string {
	if f.Posture == PosturePermissive {
		return PosturePermissive
	}
	return PostureSecure
}

// PostureFor is the posture target-scoped evaluation uses for req, and the
// posture rules that decided it. It starts from the global posture; a
// matching rule that says secure makes it secure, and wins over any that say
// permissive (the most restrictive reading, as policy combines). A rule
// that says permissive applies only to a target whose env and owner are both
// declared and that is not ad hoc: a scoped relaxation never reaches a
// target nobody labeled (Decision 17), however broad its match, because
// `env: "!prod"` matches an unknown env too. The global posture is the one
// switch that opens everything.
func (f File) PostureFor(req Request) (string, []int) {
	posture := f.GlobalPosture()
	var secure, permissive []int
	for i, r := range f.PostureRules {
		if !r.Match.Matches(req.Connector, req.Target) {
			continue
		}
		switch {
		case r.Posture == PostureSecure:
			secure = append(secure, i)
		case r.Posture == PosturePermissive && scopable(req.Target):
			permissive = append(permissive, i)
		}
	}
	switch {
	case len(secure) > 0:
		return PostureSecure, secure
	case len(permissive) > 0:
		return PosturePermissive, permissive
	}
	return posture, nil
}

// scopable reports whether a scoped permissive rule may apply to t: it is
// labeled, with a known env and owner, and it is a registered target.
func scopable(t target.Target) bool {
	known := func(v string) bool { return v != "" && !strings.EqualFold(v, "unknown") }
	return known(string(t.Env)) && known(t.Owner) && !t.Adhoc
}

// BaselineOverride replaces cells of the built-in baseline table.
type BaselineOverride struct {
	ByEffect map[contract.Effect]map[string]Decision `yaml:"by_effect,omitempty"`
}

// Provider is one connector's or plugin's profile.
type Provider struct {
	Rules []Rule `yaml:"rules"`
}

// TargetBlock is rules for targets that match.
type TargetBlock struct {
	Match TargetMatch `yaml:"match"`
	Rules []Rule      `yaml:"rules"`
}

// PrincipalBlock is rules for principals that match, and grants they hold.
// A grant is not an allow: it lifts one built-in refusal (adhoc_targets
// lifts builtin.adhoc), because an allow can never lower a stricter match.
type PrincipalBlock struct {
	Match  PrincipalMatch `yaml:"match"`
	Grants []string       `yaml:"grants,omitempty"`
	Rules  []Rule         `yaml:"rules"`
}

// Rule is one decision for operations that match it. Ops and Effect narrow
// which operations; Principal narrows who asks. An empty list matches all.
type Rule struct {
	ID        string            `yaml:"id,omitempty"`
	Ops       []string          `yaml:"ops,omitempty"`
	Effect    []contract.Effect `yaml:"effect,omitempty"`
	Principal *PrincipalMatch   `yaml:"principal,omitempty"`
	Decision  Decision          `yaml:"decision"`
	Approval  *Approval         `yaml:"approval,omitempty"`
	Reason    string            `yaml:"reason,omitempty"`
}

// TargetMatch selects targets. Each set field must match; a value prefixed
// with "!" matches anything else. Tags must all be present.
type TargetMatch struct {
	ID        string   `yaml:"id,omitempty"`
	Connector string   `yaml:"connector,omitempty"`
	Kind      string   `yaml:"kind,omitempty"`
	Env       string   `yaml:"env,omitempty"`
	Owner     string   `yaml:"owner,omitempty"`
	Admin     string   `yaml:"admin,omitempty"`
	Tags      []string `yaml:"tags,omitempty"`
	Adhoc     *bool    `yaml:"adhoc,omitempty"`
}

// PrincipalMatch selects principals. Client and session may end in "*".
type PrincipalMatch struct {
	Kind    string `yaml:"kind,omitempty"`
	Client  string `yaml:"client,omitempty"`
	Session string `yaml:"session,omitempty"`
}

// matches reports whether a rule covers this operation and principal.
func (r Rule) matches(req Request) bool {
	if len(r.Ops) > 0 && !containsFold(r.Ops, req.Operation) {
		return false
	}
	if len(r.Effect) > 0 {
		found := false
		for _, e := range r.Effect {
			if e == effectOf(req) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if r.Principal != nil && !r.Principal.matches(req.Principal) {
		return false
	}
	return true
}

// Matches reports whether a resolved target matches. It is the one target
// matcher: policy target rules and posture rules both use it, so posture
// scoping and policy scoping cannot disagree. Unknown is a value like any
// other (a target with no env matches `env: unknown` and `env: "!prod"`),
// and adhoc matches only when the match says so.
func (m TargetMatch) Matches(connector string, t target.Target) bool {
	checks := []struct{ want, got string }{
		{m.ID, t.ID}, {m.Connector, connector}, {m.Kind, t.Kind},
		{m.Env, string(t.Env)}, {m.Owner, t.Owner}, {m.Admin, t.AdminFor},
	}
	for _, c := range checks {
		if c.want != "" && !valueMatches(c.want, c.got) {
			return false
		}
	}
	for _, tag := range m.Tags {
		if !containsFold(t.Tags, tag) {
			return false
		}
	}
	if m.Adhoc != nil && *m.Adhoc != t.Adhoc {
		return false
	}
	return true
}

func (m PrincipalMatch) matches(p Principal) bool {
	return (m.Kind == "" || valueMatches(m.Kind, p.Kind)) &&
		(m.Client == "" || globMatches(m.Client, p.Client)) &&
		(m.Session == "" || globMatches(m.Session, p.Session))
}

// valueMatches is equality, or inequality for a "!"-prefixed want.
func valueMatches(want, got string) bool {
	if strings.HasPrefix(want, "!") {
		return !strings.EqualFold(strings.TrimPrefix(want, "!"), got)
	}
	return strings.EqualFold(want, got)
}

func globMatches(pattern, got string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(strings.ToLower(got), strings.ToLower(strings.TrimSuffix(pattern, "*")))
	}
	return strings.EqualFold(pattern, got)
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// effectOf is the request's effect, with unknown as exec: the strict
// reading of an operation that declares none (as the contract reads it).
func effectOf(req Request) contract.Effect {
	if req.Effect == "" || !req.Effect.Valid() {
		return ""
	}
	return req.Effect
}

// Validate reports every problem in a file at once.
func (f File) Validate() []string {
	var problems []string
	if f.Version != FileVersion {
		problems = append(problems, fmt.Sprintf("version must be %d", FileVersion))
	}
	if f.Posture != "" && f.Posture != PostureSecure && f.Posture != PosturePermissive {
		problems = append(problems, fmt.Sprintf("posture %q is not secure or permissive", f.Posture))
	}
	for i, r := range f.PostureRules {
		if r.Posture != PostureSecure && r.Posture != PosturePermissive {
			problems = append(problems, fmt.Sprintf("posture_rules[%d].posture %q is not secure or permissive", i, r.Posture))
		}
	}
	check := func(where string, rules []Rule) {
		for i, r := range rules {
			at := fmt.Sprintf("%s.rules[%d]", where, i)
			if r.ID != "" {
				at += " (" + r.ID + ")"
			}
			if !r.Decision.Valid() {
				problems = append(problems, fmt.Sprintf("%s: decision %q is not allow, dry_run_only, approve or deny", at, r.Decision))
			}
			for _, e := range r.Effect {
				if !e.Valid() {
					problems = append(problems, fmt.Sprintf("%s: effect %q is not an effect class", at, e))
				}
			}
			if r.Principal != nil && r.Principal.Kind != "" && !validKind(strings.TrimPrefix(r.Principal.Kind, "!")) {
				problems = append(problems, fmt.Sprintf("%s: principal kind %q is not human, agent or automation", at, r.Principal.Kind))
			}
		}
	}
	if f.Baseline != nil {
		for effect, cells := range f.Baseline.ByEffect {
			if !effect.Valid() {
				problems = append(problems, fmt.Sprintf("baseline.by_effect: %q is not an effect class", effect))
			}
			for kind, d := range cells {
				if !validKind(kind) {
					problems = append(problems, fmt.Sprintf("baseline.by_effect.%s: %q is not human, agent or automation", effect, kind))
				}
				if !d.Valid() {
					problems = append(problems, fmt.Sprintf("baseline.by_effect.%s.%s: decision %q is not allow, dry_run_only, approve or deny", effect, kind, d))
				}
			}
		}
	}
	for id, p := range f.Providers {
		check("providers."+id, p.Rules)
	}
	for i, t := range f.Targets {
		check(fmt.Sprintf("targets[%d]", i), t.Rules)
	}
	for i, p := range f.Principals {
		if p.Match.Kind != "" && !validKind(strings.TrimPrefix(p.Match.Kind, "!")) {
			problems = append(problems, fmt.Sprintf("principals[%d].match.kind %q is not human, agent or automation", i, p.Match.Kind))
		}
		for _, g := range p.Grants {
			if g != "adhoc_targets" {
				problems = append(problems, fmt.Sprintf("principals[%d].grants: %q is not a grant (adhoc_targets)", i, g))
			}
		}
		check(fmt.Sprintf("principals[%d]", i), p.Rules)
	}
	return problems
}

func validKind(k string) bool { return k == "human" || k == "agent" || k == "automation" }

// Merge combines files. Rules accumulate; a later baseline cell replaces an
// earlier one. Nothing a later file says can remove a rule an earlier one
// made, so merging can only add restrictions or change baseline cells.
func Merge(files ...File) File {
	out := File{Version: FileVersion, Providers: map[string]Provider{}}
	for _, f := range files {
		if f.Posture != "" {
			out.Posture = f.Posture
		}
		out.PostureRules = append(out.PostureRules, f.PostureRules...)
		if f.Baseline != nil {
			if out.Baseline == nil {
				out.Baseline = &BaselineOverride{ByEffect: map[contract.Effect]map[string]Decision{}}
			}
			for effect, cells := range f.Baseline.ByEffect {
				if out.Baseline.ByEffect[effect] == nil {
					out.Baseline.ByEffect[effect] = map[string]Decision{}
				}
				for kind, d := range cells {
					out.Baseline.ByEffect[effect][kind] = d
				}
			}
		}
		for id, p := range f.Providers {
			cur := out.Providers[id]
			cur.Rules = append(cur.Rules, p.Rules...)
			out.Providers[id] = cur
		}
		out.Targets = append(out.Targets, f.Targets...)
		out.Principals = append(out.Principals, f.Principals...)
	}
	return out
}

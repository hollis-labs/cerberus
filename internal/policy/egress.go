package policy

import (
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/target"
)

// EgressRule shapes what comes back (P4-4, section 7): for a labeled part of
// a result (untrusted or personal), for a principal, on a target, pass it,
// cap it, mask it or refuse it. The default is pass (the operator's P4
// decision 2): output is delivered, marked. A rule is recorded in shadow
// until it says mode: enforce, and what it withholds is always said, never
// silently dropped.
//
//	egress:
//	  - id: agent-logs-off-dev
//	    match: { env: "!dev" }
//	    principal: { kind: agent }
//	    label: untrusted
//	    action: cap
//	    lines: 200
//	    mode: enforce
type EgressRule struct {
	ID        string          `yaml:"id,omitempty"`
	Match     TargetMatch     `yaml:"match,omitempty"`
	Principal *PrincipalMatch `yaml:"principal,omitempty"`
	Label     string          `yaml:"label"`
	Action    string          `yaml:"action"`
	// Lines is cap's limit: lines of a text field, or elements of a list.
	Lines int    `yaml:"lines,omitempty"`
	Mode  string `yaml:"mode,omitempty"`
}

// Egress actions, least to most restrictive.
const (
	EgressPass   = "pass"
	EgressCap    = "cap"
	EgressMask   = "mask"
	EgressRefuse = "refuse"
)

// Egress modes.
const (
	EgressShadow  = "shadow"
	EgressEnforce = "enforce"
)

var egressLabels = []string{"untrusted", "personal"}

func egressRank(action string) int {
	switch action {
	case EgressCap:
		return 1
	case EgressMask:
		return 2
	case EgressRefuse:
		return 3
	default:
		return 0
	}
}

// EgressDecision is what policy says about one label of a result.
type EgressDecision struct {
	Rule   string `json:"rule,omitempty"`
	Label  string `json:"label"`
	Action string `json:"action"`
	Lines  int    `json:"lines,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

// Enforced is whether the decision changes what is returned.
func (d EgressDecision) Enforced() bool {
	return d.Mode == EgressEnforce && d.Action != EgressPass
}

// EgressFor is the decision for label on a result of connector's operation
// on t, for a principal of kind: the most restrictive matching rule, the
// smaller cap between two caps, and enforce if any rule at that strength
// enforces. No match is pass.
func (f File) EgressFor(connector string, t target.Target, kind, label string) EgressDecision {
	best := EgressDecision{Label: label, Action: EgressPass}
	for i, r := range f.Egress {
		if r.Label != label || !r.Match.Matches(connector, t) {
			continue
		}
		if r.Principal != nil && !r.Principal.matches(Principal{Kind: kind}) {
			continue
		}
		mode := r.Mode
		if mode == "" {
			mode = EgressShadow
		}
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("egress[%d]", i)
		}
		d := EgressDecision{Rule: id, Label: label, Action: r.Action, Lines: r.Lines, Mode: mode}
		switch rb, cb := egressRank(d.Action), egressRank(best.Action); {
		case rb > cb:
			best = d
		case rb == cb && rb > 0:
			if d.Action == EgressCap && d.Lines < best.Lines {
				best.Lines, best.Rule = d.Lines, d.Rule
			}
			if d.Mode == EgressEnforce {
				best.Mode = EgressEnforce
			}
		}
	}
	return best
}

func egressProblems(rules []EgressRule) []string {
	var problems []string
	for i, r := range rules {
		at := fmt.Sprintf("egress[%d]", i)
		if r.ID != "" {
			at += " (" + r.ID + ")"
		}
		if !contains(egressLabels, r.Label) {
			problems = append(problems, fmt.Sprintf("%s: label %q is not untrusted or personal", at, r.Label))
		}
		switch r.Action {
		case EgressPass, EgressMask, EgressRefuse:
			if r.Lines != 0 {
				problems = append(problems, fmt.Sprintf("%s: lines applies only to cap", at))
			}
		case EgressCap:
			if r.Lines <= 0 {
				problems = append(problems, fmt.Sprintf("%s: cap needs lines greater than 0", at))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s: action %q is not pass, cap, mask or refuse", at, r.Action))
		}
		if r.Mode != "" && r.Mode != EgressShadow && r.Mode != EgressEnforce {
			problems = append(problems, fmt.Sprintf("%s: mode %q is not shadow or enforce", at, r.Mode))
		}
		if r.Principal != nil && r.Principal.Kind != "" && !validKind(strings.TrimPrefix(r.Principal.Kind, "!")) {
			problems = append(problems, fmt.Sprintf("%s: principal kind %q is not human, agent or automation", at, r.Principal.Kind))
		}
		problems = append(problems, r.Match.problems(at+".match")...)
	}
	return problems
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

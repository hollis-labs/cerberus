package policy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
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
	// Effect narrows the rule to operations of these effects; empty is any.
	Effect []contract.Effect `yaml:"effect,omitempty"`
	Label  string            `yaml:"label"`
	Action string            `yaml:"action"`
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
	// ShadowAction and ShadowRule are a shadow rule stricter than the
	// enforced decision: recorded, as what it would do, never applied.
	ShadowAction string `json:"shadow_action,omitempty"`
	ShadowRule   string `json:"shadow_rule,omitempty"`
}

// Enforced is whether the decision changes what is returned.
func (d EgressDecision) Enforced() bool {
	return d.Mode == EgressEnforce && d.Action != EgressPass
}

// EgressFor is the decision for label on a result of connector's operation,
// of effect, on t, for a principal of kind. What is applied is the most
// restrictive matching rule that enforces, with the smaller cap between two
// caps; with none enforcing, the most restrictive shadow rule, recorded and
// not applied. A stronger shadow rule never displaces an enforced one: it
// used to, so a shadow refuse beside an enforced mask left the result
// unmasked (M-13). It is recorded alongside, in ShadowAction. No match is
// pass.
func (f File) EgressFor(connector string, t target.Target, kind string, effect contract.Effect, label string) EgressDecision {
	enforced := EgressDecision{Label: label, Action: EgressPass}
	shadow := EgressDecision{Label: label, Action: EgressPass}
	for i, r := range f.Egress {
		if r.Label != label || !r.Match.Matches(connector, t) {
			continue
		}
		if len(r.Effect) > 0 && !slices.Contains(r.Effect, effect) {
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
		if mode == EgressEnforce {
			enforced = stricter(enforced, d)
		} else {
			shadow = stricter(shadow, d)
		}
	}
	if enforced.Action == EgressPass {
		return shadow
	}
	enforced.Mode = EgressEnforce
	if egressRank(shadow.Action) > egressRank(enforced.Action) {
		enforced.ShadowAction, enforced.ShadowRule = shadow.Action, shadow.Rule
	}
	return enforced
}

// stricter is the more restrictive of two decisions for the same label:
// the higher-ranked action, and between two caps the smaller.
func stricter(best, d EgressDecision) EgressDecision {
	switch rb, cb := egressRank(d.Action), egressRank(best.Action); {
	case rb > cb:
		return d
	case rb == cb && rb > 0 && d.Action == EgressCap && d.Lines < best.Lines:
		best.Lines, best.Rule = d.Lines, d.Rule
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
		for _, e := range r.Effect {
			if !e.Valid() {
				problems = append(problems, fmt.Sprintf("%s: effect %q is not an effect class", at, e))
			}
		}
		problems = append(problems, r.Match.problems(at+".match")...)
	}
	return problems
}

// EgressWarnings are the refuse rules that can match an operation that is
// not a read. There a refusal would come after the operation changed
// something, and a caller told it failed would run it again; so for those
// effects refuse withholds the output and still reports success.
func (f File) EgressWarnings() []string {
	var out []string
	for i, r := range f.Egress {
		if r.Action != EgressRefuse {
			continue
		}
		var nonRead []string
		if len(r.Effect) == 0 {
			nonRead = []string{"any effect"}
		}
		for _, e := range r.Effect {
			if !e.ReadOnly() {
				nonRead = append(nonRead, string(e))
			}
		}
		if len(nonRead) == 0 {
			continue
		}
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("egress[%d]", i)
		}
		out = append(out, fmt.Sprintf("egress rule %s refuses %s output and can match operations that are not reads (%s): those have already run, so there it withholds the output and reports success rather than an error; add effect: [read, read_sensitive] to refuse only reads",
			id, r.Label, strings.Join(nonRead, ", ")))
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

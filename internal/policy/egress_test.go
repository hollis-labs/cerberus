package policy

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestEgressRulesAreValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		rule EgressRule
		want string
	}{
		"a label typo":      {EgressRule{Label: "untrustd", Action: EgressPass}, `label "untrustd"`},
		"an action typo":    {EgressRule{Label: "untrusted", Action: "trim"}, `action "trim"`},
		"cap without lines": {EgressRule{Label: "untrusted", Action: EgressCap}, "cap needs lines"},
		"lines on mask":     {EgressRule{Label: "untrusted", Action: EgressMask, Lines: 5}, "lines applies only to cap"},
		"a mode typo":       {EgressRule{Label: "personal", Action: EgressMask, Mode: "on"}, `mode "on"`},
		"a kind typo":       {EgressRule{Label: "personal", Action: EgressMask, Principal: &PrincipalMatch{Kind: "bot"}}, `principal kind "bot"`},
		"a match env typo":  {EgressRule{Label: "personal", Action: EgressMask, Match: TargetMatch{Env: "production"}}, `.match.env "production"`},
	} {
		f := File{Version: FileVersion, Egress: []EgressRule{tc.rule}}
		if got := strings.Join(f.Validate(), "\n"); !strings.Contains(got, tc.want) {
			t.Errorf("%s: want %q in %q", name, tc.want, got)
		}
	}
	ok := File{Version: FileVersion, Egress: []EgressRule{
		{ID: "cap", Label: "untrusted", Action: EgressCap, Lines: 50, Mode: EgressEnforce, Principal: &PrincipalMatch{Kind: "agent"}, Match: TargetMatch{Env: "!dev"}},
		{Label: "personal", Action: EgressMask},
	}}
	if p := ok.Validate(); len(p) != 0 {
		t.Fatalf("valid rules refused: %v", p)
	}
	if _, err := decodeFile([]byte("version: 1\negress:\n  - { label: untrusted, action: cap, line: 5 }\n")); err == nil {
		t.Fatal("a misspelled egress key decoded")
	}
}

// The most restrictive enforced rule is applied, with the smaller limit
// between caps, and a stricter shadow rule is recorded beside it; with
// none enforced, the most restrictive shadow rule is recorded; no match
// passes; shadow is the default mode.
func TestEgressForPrecedence(t *testing.T) {
	prod := target.Target{Connector: "local", Labels: target.Labels{Env: target.EnvProd, Owner: "self"}, AdminFor: "self"}
	dev := target.Target{Connector: "local", Labels: target.Labels{Env: target.EnvDev, Owner: "self"}, AdminFor: "self"}
	agent := &PrincipalMatch{Kind: "agent"}
	f := File{Egress: []EgressRule{
		{ID: "cap-100", Label: "untrusted", Action: EgressCap, Lines: 100, Principal: agent},
		{ID: "cap-20", Label: "untrusted", Action: EgressCap, Lines: 20, Principal: agent, Mode: EgressEnforce},
		{ID: "mask-prod", Label: "untrusted", Action: EgressMask, Principal: agent, Match: TargetMatch{Env: "prod"}},
		{ID: "refuse-personal", Label: "personal", Action: EgressRefuse, Mode: EgressEnforce},
	}}
	if d := f.EgressFor("local", dev, "agent", contract.EffectRead, "untrusted"); d.Action != EgressCap || d.Lines != 20 || d.Mode != EgressEnforce || !d.Enforced() {
		t.Errorf("two caps: %+v", d)
	}
	// A stronger shadow rule never displaces an enforced one (M-13): the
	// enforced cap applies, and the shadow mask is recorded beside it.
	if d := f.EgressFor("local", prod, "agent", contract.EffectRead, "untrusted"); d.Action != EgressCap || d.Lines != 20 || !d.Enforced() ||
		d.ShadowAction != EgressMask || d.ShadowRule != "mask-prod" {
		t.Errorf("an enforced cap beside a stronger shadow mask: %+v", d)
	}
	// With nothing enforced, the strongest shadow rule is recorded.
	shadowOnly := File{Egress: []EgressRule{f.Egress[0], f.Egress[2]}}
	if d := shadowOnly.EgressFor("local", prod, "agent", contract.EffectRead, "untrusted"); d.Action != EgressMask || d.Mode != EgressShadow || d.Enforced() {
		t.Errorf("mask beats cap, and defaults to shadow: %+v", d)
	}
	if d := f.EgressFor("local", dev, "human", contract.EffectRead, "untrusted"); d.Action != EgressPass {
		t.Errorf("no rule for a human: %+v", d)
	}
	if d := f.EgressFor("local", dev, "human", contract.EffectRead, "personal"); d.Action != EgressRefuse || !d.Enforced() {
		t.Errorf("personal: %+v", d)
	}
}

// A rule narrowed by effect matches only those effects, and an effect typo
// is refused.
func TestEgressEffectNarrowing(t *testing.T) {
	dev := target.Target{Connector: "ssh", Labels: target.Labels{Env: target.EnvDev, Owner: "self"}, AdminFor: "self"}
	f := File{Version: FileVersion, Egress: []EgressRule{{ID: "reads", Effect: []contract.Effect{contract.EffectRead, contract.EffectReadSensitive}, Label: "untrusted", Action: EgressRefuse, Mode: EgressEnforce}}}
	if d := f.EgressFor("ssh", dev, "agent", contract.EffectReadSensitive, "untrusted"); d.Action != EgressRefuse {
		t.Errorf("a read: %+v", d)
	}
	if d := f.EgressFor("ssh", dev, "agent", contract.EffectExec, "untrusted"); d.Action != EgressPass {
		t.Errorf("an exec: %+v", d)
	}
	f.Egress[0].Effect = []contract.Effect{"reads"}
	if got := strings.Join(f.Validate(), " "); !strings.Contains(got, `effect "reads"`) {
		t.Errorf("an effect typo: %q", got)
	}
}

// A refuse rule that can match a non-read warns, saying what it will do
// there; one narrowed to reads does not.
func TestEgressWarnings(t *testing.T) {
	f := File{Egress: []EgressRule{
		{ID: "any", Label: "untrusted", Action: EgressRefuse},
		{ID: "some-writes", Label: "personal", Action: EgressRefuse, Effect: []contract.Effect{contract.EffectRead, contract.EffectWrite}},
		{ID: "reads-only", Label: "untrusted", Action: EgressRefuse, Effect: []contract.Effect{contract.EffectRead, contract.EffectReadSensitive}},
		{ID: "a-cap", Label: "untrusted", Action: EgressCap, Lines: 5},
	}}
	w := f.EgressWarnings()
	if len(w) != 2 || !strings.Contains(w[0], "egress rule any refuses untrusted output") || !strings.Contains(w[0], "(any effect)") ||
		!strings.Contains(w[1], "(write)") || !strings.Contains(w[0], "reports success rather than an error") {
		t.Fatalf("warnings: %v", w)
	}
}

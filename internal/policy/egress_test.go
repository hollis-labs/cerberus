package policy

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/target"
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

// The most restrictive matching rule decides; between caps the smaller
// limit; enforce if any rule at that strength enforces; no match passes;
// shadow is the default mode.
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
	if d := f.EgressFor("local", dev, "agent", "untrusted"); d.Action != EgressCap || d.Lines != 20 || d.Mode != EgressEnforce || !d.Enforced() {
		t.Errorf("two caps: %+v", d)
	}
	if d := f.EgressFor("local", prod, "agent", "untrusted"); d.Action != EgressMask || d.Rule != "mask-prod" || d.Mode != EgressShadow || d.Enforced() {
		t.Errorf("mask beats cap, and defaults to shadow: %+v", d)
	}
	if d := f.EgressFor("local", dev, "human", "untrusted"); d.Action != EgressPass {
		t.Errorf("no rule for a human: %+v", d)
	}
	if d := f.EgressFor("local", dev, "human", "personal"); d.Action != EgressRefuse || !d.Enforced() {
		t.Errorf("personal: %+v", d)
	}
}

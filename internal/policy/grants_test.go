package policy

import (
	"strings"
	"testing"
	"time"
)

// A session or window grant is flagged unless its rule can only select dev
// or lab targets that are ours and not shared; a once approval never is.
func TestGrantWarnings(t *testing.T) {
	window := &Approval{Scope: ScopeWindow, TTL: 30 * time.Minute}
	rule := func(id string, a *Approval) Rule { return Rule{ID: id, Decision: Approve, Approval: a} }
	f := File{Version: FileVersion,
		Providers: map[string]Provider{"local": {Rules: []Rule{rule("everywhere", window), rule("once", &Approval{Scope: ScopeOnce})}}},
		Targets: []TargetBlock{
			{Match: TargetMatch{Env: "dev", Owner: "self", Admin: "self"}, Rules: []Rule{rule("dev-mine", window)}},
			{Match: TargetMatch{Env: "dev", Owner: "self"}, Rules: []Rule{rule("maybe-shared", window)}},
			{Match: TargetMatch{Env: "prod"}, Rules: []Rule{rule("prod", &Approval{Scope: ScopeSession})}},
		},
		Principals: []PrincipalBlock{{Match: PrincipalMatch{Kind: "agent"}, Rules: []Rule{rule("agents", window)}}},
	}
	got := strings.Join(f.GrantWarnings(), "\n")
	for _, want := range []string{"(everywhere)", "(maybe-shared)", "(prod)", "(agents)"} {
		if !strings.Contains(got, want) {
			t.Errorf("not flagged: %s\n%s", want, got)
		}
	}
	for _, safe := range []string{"(once)", "(dev-mine)"} {
		if strings.Contains(got, safe) {
			t.Errorf("flagged: %s\n%s", safe, got)
		}
	}
	bad := File{Version: FileVersion, Providers: map[string]Provider{"local": {Rules: []Rule{rule("typo", &Approval{Scope: "forever"})}}}}
	if problems := strings.Join(bad.Validate(), "\n"); !strings.Contains(problems, `scope "forever"`) {
		t.Fatalf("an unknown scope passed validation: %s", problems)
	}
}

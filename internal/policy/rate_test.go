package policy

import (
	"strings"
	"testing"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestParseRate(t *testing.T) {
	for in, want := range map[string]Rate{"5/h": {Limit: 5, Window: time.Hour}, "1/m": {Limit: 1, Window: time.Minute}, " 30/d ": {Limit: 30, Window: 24 * time.Hour}} {
		got, err := ParseRate(in)
		if err != nil || got.Limit != want.Limit || got.Window != want.Window {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "5", "0/h", "-1/h", "5/w", "5/10m", "x/h"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// A rate rides on an allow or an approve, parses, and names its counter
// with an id of its own.
func TestRateValidation(t *testing.T) {
	file := func(rules ...Rule) File {
		return File{Version: FileVersion, Principals: []PrincipalBlock{{Match: PrincipalMatch{Kind: "agent"}, Rules: rules}}}
	}
	if p := file(Rule{ID: "a", Decision: Allow, Rate: "5/h"}, Rule{ID: "b", Decision: Approve, Rate: "1/d"}).Validate(); len(p) != 0 {
		t.Fatalf("valid rates: %v", p)
	}
	for want, f := range map[string]File{
		"not N/m":                file(Rule{ID: "a", Decision: Allow, Rate: "5/week"}),
		"not deny":               file(Rule{ID: "a", Decision: Deny, Rate: "5/h"}),
		"not dry_run_only":       file(Rule{ID: "a", Decision: DryRunOnly, Rate: "5/h"}),
		"needs an id":            file(Rule{Decision: Allow, Rate: "5/h"}),
		"already names the rate": file(Rule{ID: "a", Decision: Allow, Rate: "5/h"}, Rule{ID: "a", Decision: Allow, Rate: "1/h"}),
	} {
		if p := strings.Join(f.Validate(), "; "); !strings.Contains(p, want) {
			t.Errorf("want %q in %q", want, p)
		}
	}
}

// A matched rule carries its rate to the gate.
func TestMatchCarriesTheRate(t *testing.T) {
	f := File{Version: FileVersion, Principals: []PrincipalBlock{{Match: PrincipalMatch{Kind: "agent"},
		Rules: []Rule{{ID: "agent-writes", Effect: []contract.Effect{contract.EffectWrite}, Decision: Allow, Rate: "3/m"}}}}}
	res := NewEvaluator(f, "t").Authorize(Request{Connector: "docker", Operation: "up", Effect: contract.EffectWrite, Principal: Principal{Kind: "agent"}})
	for _, m := range res.Matched {
		if m.Rule == "agent-writes" && m.Rate != nil && m.Rate.String() == "3/m" {
			return
		}
	}
	t.Fatalf("no rate on the match: %+v", res.Matched)
}

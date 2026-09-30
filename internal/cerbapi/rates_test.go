package cerbapi

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ratedPolicy allows an agent's lifecycle calls, at most twice an hour.
func ratedPolicy(t *testing.T) {
	t.Helper()
	f := policy.File{Version: policy.FileVersion,
		Baseline: &policy.BaselineOverride{ByEffect: map[contract.Effect]map[string]policy.Decision{contract.EffectLifecycle: {"agent": policy.Allow}}},
		Principals: []policy.PrincipalBlock{{Match: policy.PrincipalMatch{Kind: "agent"},
			Rules: []policy.Rule{{ID: "agent-lifecycle", Effect: []contract.Effect{contract.EffectLifecycle}, Decision: policy.Allow, Rate: "2/h"}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	withPDP(t, policy.NewEvaluator(f, "test"))
}

// withRates installs a limiter on a clock the test moves.
func withRates(t *testing.T, auditDir string) *time.Time {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	SetRateLimiter(&RateLimiter{AuditDir: auditDir, Now: func() time.Time { return now }})
	t.Cleanup(func() { SetRateLimiter(nil) })
	return &now
}

func ranCount(recs []audit.Record) int {
	n := 0
	for _, r := range recs {
		if r.Kind == audit.KindOutcome && r.Decision == audit.DecisionAllowed && !r.DryRun {
			n++
		}
	}
	return n
}

var agentSession = Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code", Session: "s1"}

// Enforced, a spent rate refuses as policy_denied naming when the next call
// is allowed; the refusal is not counted; another session has its own
// count; and the window slides.
func TestEnforcedRateLimitRefusesOnceSpent(t *testing.T) {
	ratedPolicy(t)
	withEnforcement(t, nil)
	now := withRates(t, "")
	sink := audit.NewMemory()
	svc, _ := dockerLane(t, sink)
	ctx := callerAs(agentSession, SurfaceSocket)
	for i := 0; i < 2; i++ {
		if _, err := svc.Execute(ctx, devStop()); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		*now = now.Add(time.Minute)
	}
	_, err := svc.Execute(ctx, devStop())
	if connectorErrorCode(err) != ExternalConnectorPolicyDenied || !strings.Contains(err.Error(), "rate limit: rule agent-lifecycle allows 2/h") ||
		!strings.Contains(err.Error(), "the next is allowed at "+time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC).Local().Format(time.RFC3339)) {
		t.Fatalf("a third call: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
	if ranCount(sink.Records()) != 2 {
		t.Fatalf("%d calls ran", ranCount(sink.Records()))
	}
	other := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code", Session: "s2"}, SurfaceSocket)
	if _, err = svc.Execute(other, devStop()); err != nil {
		t.Fatalf("another session: %v", err)
	}
	// The refused call gave its count back: the first leaves the window at
	// 13:00, and one call is then allowed, not zero.
	*now = time.Date(2026, 9, 29, 13, 0, 30, 0, time.UTC)
	if _, err = svc.Execute(ctx, devStop()); err != nil {
		t.Fatalf("after the window slid: %v", err)
	}
	if _, err = svc.Execute(ctx, devStop()); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatalf("the slid window is full again: %v", err)
	}
}

// In shadow, a spent rate is a recorded would-block, and the call runs.
func TestShadowRateLimitIsAWouldBlock(t *testing.T) {
	ratedPolicy(t)
	withRates(t, "")
	sink := audit.NewMemory()
	svc, _ := dockerLane(t, sink)
	ctx := callerAs(agentSession, SurfaceSocket)
	for i := 0; i < 3; i++ {
		if _, err := svc.Execute(ctx, devStop()); err != nil {
			t.Fatalf("call %d in shadow: %v", i+1, err)
		}
	}
	var intents []audit.Record
	for _, r := range sink.Records() {
		if r.Kind == audit.KindIntent {
			intents = append(intents, r)
		}
	}
	last := intents[len(intents)-1].Policy
	if ranCount(sink.Records()) != 3 || last == nil || !last.WouldBlock || last.Decision != string(policy.Deny) || !hasRule(last, "rate.agent-lifecycle") {
		t.Fatalf("the third call's shadow decision: %+v", last)
	}
	if first := intents[0].Policy; first.WouldBlock || hasRule(first, "rate.agent-lifecycle") {
		t.Fatalf("the first call's shadow decision: %+v", first)
	}
}

func hasRule(d *audit.PolicyDecision, rule string) bool {
	for _, m := range d.MatchedRules {
		if m.Rule == rule {
			return true
		}
	}
	return false
}

// A new process counts what the audit log says already ran.
func TestRateLimitSeedsFromTheAuditLog(t *testing.T) {
	ratedPolicy(t)
	withEnforcement(t, nil)
	dir := t.TempDir()
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := dockerLane(t, sink)
	ctx := callerAs(agentSession, SurfaceSocket)
	now := time.Now()
	SetRateLimiter(&RateLimiter{Now: func() time.Time { return now }})
	t.Cleanup(func() { SetRateLimiter(nil) })
	for i := 0; i < 2; i++ {
		if _, err = svc.Execute(ctx, devStop()); err != nil {
			t.Fatal(err)
		}
	}
	// A restarted daemon: a fresh limiter, seeded from the log.
	SetRateLimiter(&RateLimiter{AuditDir: dir, Now: func() time.Time { return now.Add(time.Minute) }})
	if _, err = svc.Execute(ctx, devStop()); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatalf("after a restart: %v", err)
	}
	// And a day on, the log no longer counts.
	SetRateLimiter(&RateLimiter{AuditDir: dir, Now: func() time.Time { return now.Add(2 * time.Hour) }})
	if _, err = svc.Execute(ctx, devStop()); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// Dry runs and automation are never counted or refused by a rate.
func TestRatesSkipDryRunsAndAutomation(t *testing.T) {
	ratedPolicy(t)
	l := &RateLimiter{}
	SetRateLimiter(l)
	t.Cleanup(func() { SetRateLimiter(nil) })
	res := policy.Result{Matched: []policy.Match{{Rule: "r", Decision: policy.Allow, Rate: &policy.Rate{Limit: 1, Window: time.Hour}}}}
	ctx := callerAs(agentSession, SurfaceSocket)
	for _, spec := range []auditSpec{{dryRun: true}, {planOnly: true}, {automation: true}} {
		if holds := rateHolds(ctx, spec, res); len(holds) != 0 {
			t.Fatalf("%+v held %v", spec, holds)
		}
	}
	if len(rateHolds(ctx, auditSpec{}, res)) != 1 {
		t.Fatal("a real call held nothing")
	}
}

// A call refused after it took its hold (by the connector's own checks,
// say) gives the count back; one that ran and failed keeps it.
func TestRefusedCallsGiveTheirCountBack(t *testing.T) {
	l := &RateLimiter{}
	h := rateHold{rule: "r", rate: policy.Rate{Limit: 5, Window: time.Hour}, key: "k"}
	args := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop"}
	finish := func(err error) {
		c := &auditCall{sink: audit.NewMemory(), logger: slog.Default(), rates: l.hold([]rateHold{h}), limiter: l}
		c.finish(err)
	}
	finish(externalConnectorError(args, ExternalConnectorAckRequired, errors.New("needs --ack")))
	if n, _ := l.count(h); n != 0 {
		t.Fatalf("a refused call kept its count: %d", n)
	}
	finish(externalConnectorError(args, ExternalConnectorOperationFailed, errors.New("docker said no")))
	finish(nil)
	if n, _ := l.count(h); n != 2 {
		t.Fatalf("calls that ran counted %d, not 2", n)
	}
}

// A plugin's preview reaches the plugin, which holds its credentials, so
// a rate that covers it refuses it once spent (M-8). Previews were counted
// but never refused, and the count was not seeded from the log. Here two
// earlier previews in the audit log spend the rate, and a third is denied.
func TestAPluginPreviewIsRatedLikeACall(t *testing.T) {
	f := policy.File{Version: policy.FileVersion, Principals: []policy.PrincipalBlock{{Match: policy.PrincipalMatch{Kind: "agent"},
		Rules: []policy.Rule{{ID: "previews", Decision: policy.Allow, Rate: "2/h"}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	withPDP(t, policy.NewEvaluator(f, "test"))
	dir := t.TempDir()
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	withRates(t, dir)
	SetRateLimiter(&RateLimiter{AuditDir: dir})
	p := principalFor(callerAs(agentSession, SurfaceSocket), auditSpec{})
	for i := 0; i < 2; i++ {
		if _, err := sink.Write(audit.Record{Kind: audit.KindOutcome, Principal: p, Connector: "demo", Operation: "wipe", Effect: "destructive",
			DryRun: true, Preview: audit.PreviewPluginClaimed, Decision: audit.DecisionAllowed, OutcomeCode: audit.OutcomeOK,
			Policy: &audit.PolicyDecision{Decision: "allow", MatchedRules: []audit.MatchedRule{{Rule: "previews", Decision: "allow"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	spec := auditSpec{connector: "demo", operation: "wipe", dryRun: true, preview: audit.PreviewPluginClaimed}
	req := policy.Request{Connector: "demo", Operation: "wipe", Effect: contract.EffectReadSensitive, DryRun: true, Principal: policy.Principal{Kind: "agent", Session: "s1", Client: "claude-code"}}
	res := authorizeRated(callerAs(agentSession, SurfaceSocket), spec, req)
	if res.Decision != policy.Deny || !strings.Contains(fmt.Sprint(res.Matched), "rate limit") {
		t.Fatalf("a third preview: %+v", res)
	}
	// A dry run served by the host's own preview is still not rated.
	host := authorizeRated(callerAs(agentSession, SurfaceSocket), auditSpec{connector: "demo", operation: "wipe", dryRun: true}, req)
	if host.Decision == policy.Deny {
		t.Fatalf("a host preview was rated: %+v", host)
	}
}

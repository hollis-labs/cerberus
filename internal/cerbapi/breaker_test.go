package cerbapi

import (
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// breakerPolicy denies an agent's lifecycle calls and trips after three
// real denials in ten minutes.
func breakerPolicy(t *testing.T) {
	t.Helper()
	f := policy.File{Version: policy.FileVersion, CircuitBreaker: &policy.CircuitBreaker{Denials: 3, Window: 10 * time.Minute},
		Principals: []policy.PrincipalBlock{{Match: policy.PrincipalMatch{Kind: "agent"},
			Rules: []policy.Rule{{ID: "no-agent-lifecycle", Effect: []contract.Effect{contract.EffectLifecycle}, Decision: policy.Deny, Reason: "not for agents"}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	withPDP(t, policy.NewEvaluator(f, "test"))
}

// withBreakerClock sets the breaker's clock and starts the counts fresh.
func withBreakerClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now()
	old := breakerNow
	breakerNow = func() time.Time { return now }
	denials = breakerCounts{}
	t.Cleanup(func() { breakerNow = old; denials = breakerCounts{} })
	return &now
}

func suspensions(store brake.Store) []brake.Suspension {
	st, _ := store.Load()
	return st.Suspensions
}

// Three real denials suspend the session: its next call is refused as
// session_suspended naming the reset command, in every mode; a plain read
// still runs; another session is untouched; and the trip is recorded.
func TestBreakerSuspendsAnAgentSession(t *testing.T) {
	breakerPolicy(t)
	withEnforcement(t, nil)
	store := withBrakes(t)
	withBreakerClock(t)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(agentSession, SurfaceSocket)
	for i := 0; i < 3; i++ {
		if _, err := svc.Execute(ctx, devStop()); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
			t.Fatalf("denial %d: %v", i+1, err)
		}
	}
	sus := suspensions(store)
	if len(sus) != 1 || sus[0].Key != callerKey(principalFor(ctx, auditSpec{})) || sus[0].Denials != 3 {
		t.Fatalf("suspensions: %+v", sus)
	}
	SetEnforcement(nil) // shadow: a suspension holds in every mode
	_, err := svc.Execute(ctx, devStop())
	if connectorErrorCode(err) != ExternalConnectorSessionSuspended || !strings.Contains(err.Error(), "cerberus breaker reset "+sus[0].ID) {
		t.Fatalf("a suspended session's call: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
	if _, err = svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("a plain read while suspended: %v", err)
	}
	other := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code", Session: "s2"}, SurfaceSocket)
	if _, err = svc.Execute(other, devStop()); connectorErrorCode(err) == ExternalConnectorSessionSuspended {
		t.Fatalf("another session was suspended: %v", err)
	}
	var tripped bool
	for _, r := range sink.Records() {
		tripped = tripped || (r.Kind == audit.KindBrakeChanged && r.Operation == "suspend" && strings.Contains(r.Note, "cerberus breaker reset "+sus[0].ID))
	}
	if !tripped {
		t.Fatal("the trip was not recorded")
	}
}

// Shadow would-blocks, denials of a person, and denials spread wider than
// the window do not trip it.
func TestBreakerCountsOnlyRealAgentDenialsInTheWindow(t *testing.T) {
	breakerPolicy(t)
	store := withBrakes(t)
	now := withBreakerClock(t)
	sink := audit.NewMemory()
	svc, _ := dockerLane(t, sink)
	agent := callerAs(agentSession, SurfaceSocket)
	for i := 0; i < 5; i++ {
		if _, err := svc.Execute(agent, devStop()); err != nil {
			t.Fatalf("shadow call %d: %v", i+1, err)
		}
	}
	if len(suspensions(store)) != 0 {
		t.Fatal("shadow would-blocks tripped the breaker")
	}
	withEnforcement(t, nil)
	f := policy.File{Version: policy.FileVersion, CircuitBreaker: &policy.CircuitBreaker{Denials: 3, Window: 10 * time.Minute}}
	withPDP(t, denyWithBreaker{constantPDP{decision: policy.Deny}, f})
	human := callerAs(confirmHuman, SurfaceSocket)
	for i := 0; i < 5; i++ {
		_, _ = svc.Execute(human, devStop())
	}
	if len(suspensions(store)) != 0 {
		t.Fatal("a person was suspended")
	}
	for i := 0; i < 4; i++ {
		_, _ = svc.Execute(agent, devStop())
		*now = now.Add(6 * time.Minute)
	}
	if len(suspensions(store)) != 0 {
		t.Fatal("denials spread past the window tripped it")
	}
}

// denyWithBreaker denies everything and carries a breaker in its file.
type denyWithBreaker struct {
	constantPDP
	file policy.File
}

func (d denyWithBreaker) File() policy.File { return d.file }

// A new process counts the real denials the audit log already holds.
func TestBreakerSeedsFromTheAuditLog(t *testing.T) {
	breakerPolicy(t)
	withEnforcement(t, nil)
	dir := t.TempDir()
	sink, err := audit.OpenFileSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := brake.Store{Dir: t.TempDir()}
	SetBrakes(&Brakes{Store: store, AuditDir: dir, Sink: sink})
	t.Cleanup(func() { SetBrakes(nil) })
	withBreakerClock(t)
	svc, _ := dockerLane(t, sink)
	ctx := callerAs(agentSession, SurfaceSocket)
	for i := 0; i < 2; i++ {
		_, _ = svc.Execute(ctx, devStop())
	}
	denials = breakerCounts{} // a restart
	if _, err = svc.Execute(ctx, devStop()); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatal(err)
	}
	if len(suspensions(store)) != 1 {
		t.Fatal("the breaker did not count the denials before the restart")
	}
}

// A person resets a suspension by typing the phrase; an agent cannot, and
// a wrong phrase resets nothing.
func TestBreakerResetIsAPersonsAct(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	_, x, err := store.Suspend("agent|mcp_stdio|session:s1", audit.Principal{Kind: "agent", Via: ViaMCPStdio, Session: "s1"}, 3, "10m")
	if err != nil {
		t.Fatal(err)
	}
	agent := callerAs(agentSession, SurfaceSocket)
	if _, err = ResetSuspension(agent, sink, store, x.ID, "reset "+x.ID); connectorErrorCode(err) != ExternalConnectorApprovalRequired {
		t.Fatalf("an agent resetting: %v", err)
	}
	human := callerAs(confirmHuman, SurfaceSocket)
	if _, err = ResetSuspension(human, sink, store, x.ID, "yes"); connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), "reset "+x.ID) {
		t.Fatalf("a wrong phrase: %v", err)
	}
	if len(suspensions(store)) != 1 {
		t.Fatal("a refused reset reset it")
	}
	st, err := ResetSuspension(human, sink, store, x.ID, "reset "+x.ID)
	if err != nil || len(st.Suspensions) != 0 {
		t.Fatalf("reset: %v", err)
	}
	if _, err = ResetSuspension(human, sink, store, x.ID, "reset "+x.ID); err == nil {
		t.Fatal("a second reset of the same suspension")
	}
}

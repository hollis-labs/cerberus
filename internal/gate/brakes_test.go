package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
)

// A lockdown refuses every non-read call, in shadow mode too, and records
// the refusal; a plain read still runs, and so does a policy change.
func TestLockdownRefusesAllButReads(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	if _, _, err := EngageLockdown(ctx, sink, store, "incident"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Execute(ctx, devStop())
	if connectorErrorCode(err) != CodeLockdown || !strings.Contains(err.Error(), "LOCKDOWN") || !strings.Contains(err.Error(), "incident") || backend.stopped != "" {
		t.Fatalf("stop under lockdown: %v", err)
	}
	if o := outcome(sink.Records()); o.Decision != audit.DecisionRefused || o.OutcomeCode != string(CodeLockdown) {
		t.Fatalf("outcome %+v", o)
	}
	if _, err := svc.Execute(ctx, laneArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("a read under lockdown: %v", err)
	}
	if gateFor(sink).brakeRefusal(context.Background(), Spec{Connector: "policy", Operation: "apply"}, policyTargetFor("x"), false) != nil {
		t.Fatal("a policy change was braked")
	}
	var changed bool
	for _, r := range sink.Records() {
		changed = changed || (r.Kind == audit.KindBrakeChanged && strings.Contains(r.Note, "LOCKDOWN engaged"))
	}
	if !changed {
		t.Fatal("engaging was not recorded")
	}
}

// A freeze refuses only the targets its match selects.
func TestFreezeRefusesItsTargets(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	ctx := callerAs(confirmHuman, SurfaceSocket)
	if _, _, err := EngageFreeze(ctx, sink, store, policy.TargetMatch{ID: "dev-box"}, "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, devStop()); connectorErrorCode(err) != CodeFrozen || backend.stopped != "" {
		t.Fatalf("a frozen target: %v", err)
	}
	other := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	if _, err := svc.Execute(ctx, other); err != nil || backend.stopped != "web" {
		t.Fatalf("an unfrozen target: %v", err)
	}
}

// withLiftBroker installs a broker, as the daemon has, for lift approvals.
func withLiftBroker(t *testing.T) *Broker {
	t.Helper()
	broker, err := NewBroker(audit.NewMemory(), filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	setBroker(broker)
	t.Cleanup(func() { setBroker(nil) })
	return broker
}

// Anyone engages; only a person lifts, and only with an out-of-band
// approval the owner decided on the console. The old build let a person at
// the CLI lift on their terminal when no passkey was enrolled; with the
// owner's console session always the proof (decision 0.1) there is no such
// floor.
func TestLiftingIsProtected(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	if _, _, err := EngageLockdown(agent, sink, store, "an agent stopping itself"); err != nil {
		t.Fatalf("an agent engaging: %v", err)
	}
	if _, err := LiftLockdown(agent, sink, store, ""); connectorErrorCode(err) != CodeApprovalRequired || !strings.Contains(err.Error(), "--approval <id>") || !strings.Contains(err.Error(), "on the console") {
		t.Fatalf("an MCP agent lifting, refused naming both owner paths: %v", err)
	}
	cli := callerAs(confirmHuman, SurfaceSocket)
	// No broker, no approval: nothing is lifted on a terminal alone.
	if st, err := LiftLockdown(cli, sink, store, ""); connectorErrorCode(err) != CodeApprovalPending || !strings.Contains(err.Error(), "holds no approvals") || st.Lockdown == nil {
		t.Fatalf("a person at the CLI with no daemon: %v", err)
	} else if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
	broker := withLiftBroker(t)
	_, err := LiftLockdown(cli, sink, store, "")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("a person at the CLI asking: %v", err)
	}
	if _, err = broker.DecideAs(asOwner(), coded.Approval.ID, ApprovalDecisionArgs{Approve: true}, ownerConsole); err != nil {
		t.Fatalf("the owner approving: %v", err)
	}
	st, err := LiftLockdown(cli, sink, store, coded.Approval.ID)
	if err != nil || st.Lockdown != nil {
		t.Fatalf("a person at the CLI with the approval: %v %+v", err, st)
	}
	var lifted bool
	for _, r := range sink.Records() {
		lifted = lifted || (r.Kind == audit.KindBrakeChanged && strings.Contains(r.Note, "LOCKDOWN lifted (approval:"+coded.Approval.ID+")"))
	}
	if !lifted {
		t.Fatal("lifting was not recorded")
	}
}

// The owner may approve their own lift, from the surface that asked (the
// owner-session rule, the passkey's in the old build); nothing lets one
// through without the owner's console session.
func TestBrakeSelfApprovalNeedsTheOwnerSession(t *testing.T) {
	store := withBrakes(t)
	broker := withLiftBroker(t)
	sink := audit.NewMemory()
	web := asOwner()
	if _, _, err := EngageLockdown(web, sink, store, "drill"); err != nil {
		t.Fatal(err)
	}
	_, err := LiftLockdown(web, sink, store, "")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil {
		t.Fatalf("a lift: %v", err)
	}
	id := coded.Approval.ID
	stillLocked := func(what string) {
		t.Helper()
		if a, _ := broker.Get(id); a.Status != approval.Pending {
			t.Fatalf("%s: the lift approval is %s", what, a.Status)
		}
		if st, lerr := LiftLockdown(web, sink, store, id); lerr == nil || st.Lockdown == nil {
			t.Fatalf("%s: lifted: %v", what, lerr)
		}
	}
	for _, p := range []Principal{humanWeb, humanCLI} {
		if _, derr := broker.DecideAs(as(p), id, ApprovalDecisionArgs{Approve: true}, OwnerSession{}); derr == nil {
			t.Fatalf("%s decided the lift without the owner's session", p.Via)
		}
	}
	stillLocked("no owner session")

	// The requester's own surface, as the owner: the owner-session rule.
	if _, err = broker.DecideAs(web, id, ApprovalDecisionArgs{Approve: true}, ownerConsole); err != nil {
		t.Fatalf("the owner approving their own lift: %v", err)
	}
	st, err := LiftLockdown(web, sink, store, id)
	if err != nil || st.Lockdown != nil {
		t.Fatalf("lift with the approval: %v", err)
	}
	if _, err = LiftLockdown(web, sink, store, id); err == nil {
		t.Fatal("the approval lifted twice")
	}
}

func policyTargetFor(id string) target.Target { return target.Target{ID: id} }

// A frozen resource is what a pipeline lane, and the monitor, check before
// they touch it (the old build's pipeline lane, which is not the gate's,
// refused the run).
func TestFrozenResourceIsReported(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	ctx := callerAs(confirmHuman, SurfaceSocket)
	if _, _, err := EngageFreeze(ctx, sink, store, policy.TargetMatch{ID: "svc"}, "hold"); err != nil {
		t.Fatal(err)
	}
	res := config.ResourceDef{ID: "svc", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}
	if f, frozen := gateFor(sink).FrozenResource(res); !frozen || f.Reason != "hold" {
		t.Fatal("a frozen resource was not reported")
	}
	other := res
	other.ID = "other"
	if _, frozen := gateFor(sink).FrozenResource(other); frozen {
		t.Fatal("an unfrozen resource was reported")
	}
}

// A lift needs an out-of-band approval, whatever the approvals store says
// (H-b). A same-uid process can write an approval met on a terminal into
// the store; the lift used to take the channel from the store and consume
// it. The channel is required at the lift.
func TestALiftRefusesAnApprovalMetOnATerminal(t *testing.T) {
	store := withBrakes(t)
	broker := withLiftBroker(t)
	sink := audit.NewMemory()
	web := as(humanWeb)
	st, l, err := EngageLockdown(web, sink, store, "drill")
	if err != nil || st.Lockdown == nil {
		t.Fatal(err)
	}
	forged, err := broker.store.Request(approval.Approval{Principal: principalFor(web, Spec{}), Connector: "brake", Operation: "lift_lockdown",
		Effect: "admin", Target: audit.Target{Kind: "brake.lockdown", Fields: map[string]string{"id": l.ID}}, ArgsDigest: l.ID,
		Channel: approval.ChannelTTYConfirm, Scope: approval.ScopeOnce}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.store.Decide(forged.ID, approval.Decision{Approve: true, By: principalFor(web, Spec{})}, nil); err != nil {
		t.Fatal(err)
	}
	st, err = LiftLockdown(web, sink, store, forged.ID)
	if err == nil || st.Lockdown == nil {
		t.Fatalf("an approval met on a terminal lifted the lockdown: %v", err)
	}
	if cur, _ := store.Load(); cur.Lockdown == nil {
		t.Fatal("the lockdown is gone")
	}
}

// The lead's ruling (a)+(b) on decision 0.1: an operator's key at the CLI lifts a brake, and
// resets a suspension, only by naming an approval the owner decided on the console. Asking
// opens one; the owner's console approval is what lets the retry through, and nothing else
// does: not a pending approval, not one the key approved itself, never an MCP client.
func TestAKeyLiftsAndResetsOnlyWithTheOwnersConsoleApproval(t *testing.T) {
	store := withBrakes(t)
	broker := withLiftBroker(t)
	sink := audit.NewMemory()
	key := callerAs(Principal{Kind: PrincipalAgent, Via: "api", Client: "key:k1", Session: "key:k1"}, SurfaceSocket)
	if _, _, err := EngageLockdown(key, sink, store, "kill switch"); err != nil {
		t.Fatalf("a key engaging: %v", err)
	}
	_, err := LiftLockdown(key, sink, store, "")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("a key asking to lift: %v", err)
	}
	if st, lerr := LiftLockdown(key, sink, store, coded.Approval.ID); lerr == nil || st.Lockdown == nil {
		t.Fatalf("a key lifted on a pending approval: %v", lerr)
	}
	if _, err = broker.DecideAs(key, coded.Approval.ID, ApprovalDecisionArgs{Approve: true}, OwnerSession{}); err == nil {
		t.Fatal("the key approved its own lift")
	}
	if _, err = broker.DecideAs(asOwner(), coded.Approval.ID, ApprovalDecisionArgs{Approve: true}, ownerConsole); err != nil {
		t.Fatalf("the owner approving: %v", err)
	}
	mcp := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "key:k1", Session: "key:k1"}, SurfaceSocket)
	if _, err = LiftLockdown(mcp, sink, store, coded.Approval.ID); connectorErrorCode(err) != CodeApprovalRequired {
		t.Fatalf("an MCP client naming the owner's approval: %v", err)
	}
	st, err := LiftLockdown(key, sink, store, coded.Approval.ID)
	if err != nil || st.Lockdown != nil {
		t.Fatalf("the key with the owner's approval: %v %+v", err, st)
	}
	if _, err := LiftLockdown(key, sink, store, coded.Approval.ID); err == nil {
		t.Fatal("an approval lifted twice")
	}
}

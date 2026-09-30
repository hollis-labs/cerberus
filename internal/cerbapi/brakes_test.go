package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/presence/presencetest"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
)

func withBrakes(t *testing.T) brake.Store {
	t.Helper()
	store := brake.Store{Dir: t.TempDir()}
	SetBrakes(&Brakes{Store: store})
	t.Cleanup(func() { SetBrakes(nil) })
	return store
}

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
	if connectorErrorCode(err) != ExternalConnectorLockdown || !strings.Contains(err.Error(), "LOCKDOWN") || !strings.Contains(err.Error(), "incident") || backend.stopped != "" {
		t.Fatalf("stop under lockdown: %v", err)
	}
	if o := outcome(sink.Records()); o.Decision != audit.DecisionRefused || o.OutcomeCode != string(ExternalConnectorLockdown) {
		t.Fatalf("outcome %+v", o)
	}
	if _, err := svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("a read under lockdown: %v", err)
	}
	if brakeRefusal(auditSpec{connector: "policy", operation: "apply"}, policyTargetFor("x"), false) != nil {
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
	if _, err := svc.Execute(ctx, devStop()); connectorErrorCode(err) != ExternalConnectorFrozen || backend.stopped != "" {
		t.Fatalf("a frozen target: %v", err)
	}
	other := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	if _, err := svc.Execute(ctx, other); err != nil || backend.stopped != "web" {
		t.Fatalf("an unfrozen target: %v", err)
	}
}

// Anyone engages; only a person lifts. With no passkey enrolled, a person
// at the CLI lifts on their terminal; the console and an agent cannot.
func TestLiftingIsProtected(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	noPasskeys(t, sink)
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	if _, _, err := EngageLockdown(agent, sink, store, "an agent stopping itself"); err != nil {
		t.Fatalf("an agent engaging: %v", err)
	}
	if _, err := LiftLockdown(agent, sink, store, ""); connectorErrorCode(err) != ExternalConnectorApprovalRequired {
		t.Fatalf("an agent lifting: %v", err)
	}
	web := WithPrincipal(BeginRequest(context.Background(), SurfaceWeb), WebSessionPrincipal("s1"))
	if _, err := LiftLockdown(web, sink, store, ""); connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), "on a terminal") {
		t.Fatalf("the console lifting with no passkey: %v", err)
	}
	st, err := LiftLockdown(callerAs(confirmHuman, SurfaceSocket), sink, store, "")
	if err != nil || st.Lockdown != nil {
		t.Fatalf("a person at the CLI: %v %+v", err, st)
	}
	var lifted bool
	for _, r := range sink.Records() {
		lifted = lifted || (r.Kind == audit.KindBrakeChanged && strings.Contains(r.Note, "LOCKDOWN lifted (tty)"))
	}
	if !lifted {
		t.Fatal("lifting was not recorded")
	}
}

// noPasskeys installs a passkey service whose registry opens and holds no
// key: the terminal floor.
func noPasskeys(t *testing.T, sink audit.Sink) string {
	t.Helper()
	dir := t.TempDir()
	SetPresence(presence.New(dir, sink, presence.Options{}))
	t.Cleanup(func() { presencePoint.Store(nil) })
	return dir
}

// The terminal floor is for a registry that opens and holds no key. With no
// passkey service, an unreadable registry or one changed outside enrollment
// (deleted, say), a lift is refused; it never falls to the floor (GAP-898).
func TestLiftFailsClosedWithoutAReadablePasskeyStore(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	cli := callerAs(confirmHuman, SurfaceSocket)
	if _, _, err := EngageLockdown(cli, sink, store, "drill"); err != nil {
		t.Fatal(err)
	}
	refused := func(what, want string) {
		t.Helper()
		st, err := LiftLockdown(cli, sink, store, "")
		if connectorErrorCode(err) != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), want) || st.Lockdown == nil {
			t.Fatalf("%s: a lift was not refused: %v", what, err)
		}
		if redact.Text(err.Error()) != err.Error() {
			t.Fatalf("%s: redaction rewrote the refusal: %q", what, redact.Text(err.Error()))
		}
	}

	presencePoint.Store(nil)
	refused("no passkey service", "passkey store unreadable")

	dir := noPasskeys(t, sink)
	registry := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(registry, []byte(`{"version":1,"keys":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registry, 0o000); err != nil {
		t.Fatal(err)
	}
	refused("an unreadable registry", "passkey store unreadable")
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}

	// A registry that had a key and was deleted outside enrollment.
	_, _, _, _ = passkeyRoutes(t, approval.ChannelOutOfBand)
	if len(ProcessPresence().Status().Keys) != 1 {
		t.Fatal("no key enrolled")
	}
	if err := os.Remove(filepath.Join(passkeyDir, "keys.json")); err != nil {
		t.Fatal(err)
	}
	refused("a registry deleted outside enrollment", "changed outside `cerberus approvals enroll`")
}

// The self-approval waiver for connector brake lets the operator approve
// their own lift, from the surface that asked; it never lets one through
// without the passkey assertion liftProof requires.
func TestBrakeSelfApprovalNeedsTheAssertion(t *testing.T) {
	store := withBrakes(t)
	post, _, key, broker := passkeyRoutes(t, approval.ChannelOutOfBand)
	sink := audit.NewMemory()
	web := as(humanWeb)
	if _, _, err := EngageLockdown(web, sink, store, "drill"); err != nil {
		t.Fatal(err)
	}
	_, err := LiftLockdown(web, sink, store, "")
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalPending || coded.Approval == nil {
		t.Fatalf("a lift with a key enrolled: %v", err)
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
		if rec := post(p, "/approvals/"+id+"/decide", ApprovalDecisionArgs{Approve: true}); rec.Code == http.StatusOK {
			t.Fatalf("%s decided the lift with no assertion: %s", p.Via, rec.Body.String())
		}
	}
	stillLocked("no assertion")
	stranger := presencetest.New(t, presence.RPID, consoleOrigin)
	if rec := post(humanWeb, "/approvals/"+id+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: assertionFor(t, post, id, stranger)}); rec.Code == http.StatusOK {
		t.Fatalf("an unenrolled key decided the lift: %s", rec.Body.String())
	}
	stillLocked("an unenrolled key")

	// The requester's own surface, with the enrolled key: the waiver.
	if rec := post(humanWeb, "/approvals/"+id+"/decide", ApprovalDecisionArgs{Approve: true, Assertion: assertionFor(t, post, id, key)}); rec.Code != http.StatusOK {
		t.Fatalf("the operator approving their own lift with the key: %d %s", rec.Code, rec.Body.String())
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

// A freeze pauses a pipeline that touches the frozen resource.
func TestFreezePausesPipelines(t *testing.T) {
	store := withBrakes(t)
	sink := audit.NewMemory()
	svc := NewResourceRuntimeService(sink)
	dir := t.TempDir()
	setPipelines(svc, []config.ResourceDef{devResource(t, nil)}, shellPipeline(dir, "ran"))
	ctx := callerAs(confirmHuman, SurfaceSocket)
	if _, _, err := EngageFreeze(ctx, sink, store, policy.TargetMatch{ID: "svc"}, "hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunPipeline(ctx, "ship", WithAcknowledged(true)); connectorErrorCode(err) != ExternalConnectorFrozen || !strings.Contains(err.Error(), "touches svc") {
		t.Fatalf("a frozen pipeline: %v", err)
	}
	if f, frozen := frozenResource(devResource(t, nil)); !frozen || f.Reason != "hold" {
		t.Fatal("the monitor would restart a frozen resource")
	}
}

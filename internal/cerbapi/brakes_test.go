package cerbapi

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
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

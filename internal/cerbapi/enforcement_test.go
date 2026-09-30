package cerbapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// snapshotLane installs, as the daemon does, a Reloading decision point
// over an applied snapshot and the snapshot's enforcement.
func snapshotLane(t *testing.T, f policy.File) policy.Store {
	t.Helper()
	store := policy.Store{Dir: t.TempDir(), AuditDir: filepath.Join(t.TempDir(), "audit")}
	if _, err := store.Apply(f); err != nil {
		t.Fatal(err)
	}
	SetPolicyDecisionPoint(policy.NewReloading(store, nil))
	SetEnforcement(SnapshotEnforcement{})
	t.Cleanup(func() { SetPolicyDecisionPoint(nil); SetEnforcement(nil) })
	return store
}

// The switch-on: exactly the snapshot's enforced scope is enforced; the
// same decision outside it stays shadow; nothing is enforced by default.
func TestSnapshotEnforcesItsScopes(t *testing.T) {
	approveStop := policy.File{Version: policy.FileVersion, Providers: map[string]policy.Provider{
		"docker": {Rules: []policy.Rule{{ID: "stops-need-approval", Ops: []string{"stop"}, Decision: policy.Approve}}}}}
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	human := callerAs(confirmHuman, SurfaceSocket)

	snapshotLane(t, approveStop)
	if _, err := svc.Execute(agent, devStop()); err != nil || backend.stopped != "web" {
		t.Fatalf("nothing enforced, the approve is shadow: %v", err)
	}

	scoped := approveStop
	scoped.Enforcement = &policy.Enforcement{Enforce: []policy.EnforceEntry{{ID: "agents", Principal: "agent"}}}
	snapshotLane(t, scoped)
	backend.stopped = ""
	if _, err := svc.Execute(agent, devStop()); connectorErrorCode(err) != ExternalConnectorApprovalRequired && connectorErrorCode(err) != ExternalConnectorApprovalPending {
		t.Fatalf("the enforced scope ran: %v", err)
	}
	if backend.stopped != "" {
		t.Fatal("an enforced approve ran without approval")
	}
	if _, err := svc.Execute(human, devStop()); err != nil || backend.stopped != "web" {
		t.Fatalf("outside the scope, shadow: %v", err)
	}
	if (SnapshotEnforcement{}).Enforced(policy.Request{Connector: "policy", Operation: "apply", Principal: policy.Principal{Kind: "agent"}}) {
		t.Fatal("a policy change was enforced")
	}
}

// A policy apply records its enforcement, and that is what a later hash
// mismatch enforces.
func TestPolicyApplyRecordsItsEnforcement(t *testing.T) {
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	store := policy.Store{Dir: t.TempDir(), AuditDir: auditDir}
	f := policy.File{Version: policy.FileVersion, Enforcement: &policy.Enforcement{Enforce: []policy.EnforceEntry{{ID: "agents-prod", Principal: "agent", Match: policy.TargetMatch{Env: "prod"}}}}}
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	if _, err := ApplyPolicy(ctx, sink, store, f, 0); err != nil {
		t.Fatal(err)
	}
	v, history := policy.LastVerified(auditDir)
	if history != policy.VerifiedApply || len(v.Enforcement.Enforce) != 1 || v.Enforcement.Enforce[0].ID != "agents-prod" {
		t.Fatalf("recorded enforcement %+v %v", v, history)
	}
	// The outcome carries the snapshot as written, under the hash the
	// intent named (M3).
	data, _ := policy.Encode(f)
	if string(v.Snapshot) != string(data) || v.Hash != policy.Hash(data) {
		t.Fatalf("recorded snapshot %q under %s", v.Snapshot, v.Hash)
	}
}

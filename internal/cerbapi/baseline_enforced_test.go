package cerbapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// The re-review's first-run repro (B2): on a default install — no policy
// applied, so shadow — an agent over MCP applied a resource labeled
// env: prod and admin: owner by setting acknowledged itself, and it ran,
// with the audit saying deny, would_block, shadow, allowed. The built-in
// protections are now enforced whatever the mode: the agent is refused
// and nothing starts. A person's call stays shadow, and an agent's change
// to a target with no such label is not enforced.
func TestAnAgentsChangeToAProdOrOwnerTargetIsRefusedByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, policy.BaselineOnly(policy.SnapshotBaseline))
	SetEnforcement(SnapshotEnforcement{})
	t.Cleanup(func() { SetEnforcement(nil) })
	dir := t.TempDir()
	marker := func(name string) string { return filepath.Join(dir, name) }
	res := func(id string, env target.Env, admin string) config.ResourceDef {
		return config.ResourceDef{ID: id, Type: "process", Connector: "local", Env: env, Owner: target.OwnerSelf, Admin: target.Admin{Default: admin},
			Config: map[string]any{"command": []string{"/usr/bin/touch", marker(id)}}}
	}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{
		res("prod-owner", target.EnvProd, target.AdminOwner),
		res("prod-self", target.EnvProd, target.AdminSelf),
		res("dev-owner", target.EnvDev, target.AdminOwner),
		{ID: "unlabeled", Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/usr/bin/touch", marker("unlabeled")}}},
	}}
	sink := audit.NewMemory()
	svc := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	for _, id := range []string{"prod-owner", "prod-self", "dev-owner"} {
		_, err := svc.ApplyResource(agent, id, WithAcknowledged(true))
		code := connectorErrorCode(err)
		if code != ExternalConnectorPolicyDenied && code != ExternalConnectorApprovalRequired && code != ExternalConnectorApprovalPending {
			t.Errorf("%s: an agent's apply was not refused: %v", id, err)
		}
		if _, statErr := os.Stat(marker(id)); statErr == nil {
			t.Errorf("%s: the process started", id)
		}
	}
	// The admin: owner repro is a deny, not an approval to ask for.
	if _, err := svc.ApplyResource(agent, "prod-owner", WithAcknowledged(true)); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Errorf("prod-owner: %v", err)
	}
	stopAll := func(ids ...string) {
		for _, id := range ids {
			_, _ = svc.StopResource(context.Background(), id, WithAcknowledged(true))
		}
	}
	t.Cleanup(func() { stopAll("unlabeled", "prod-owner") })
	// Outside the built-in protections, shadow is unchanged.
	if out, err := svc.ApplyResource(agent, "unlabeled", WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("an agent's apply of an unlabeled resource: %+v %v", out, err)
	}
	person := callerAs(confirmHuman, SurfaceSocket)
	if out, err := svc.ApplyResource(person, "prod-owner", WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("a person's apply stays shadow: %+v %v", out, err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker("prod-owner")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a person's apply did not start the process")
		}
	}
}

// The built-in protections are held by the gate itself, whatever policy
// decision point or enforcement is installed: with none installed, a deny
// on an agent's change to a prod target is still applied.
func TestTheGateHoldsTheBuiltInProtectionsItself(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, constantPDP{decision: policy.Deny})
	SetEnforcement(nil)
	marker := filepath.Join(t.TempDir(), "ran")
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "prod", Type: "process", Connector: "local", Env: target.EnvProd,
		Config: map[string]any{"command": []string{"/usr/bin/touch", marker}}}}}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	_, err := svc.ApplyResource(callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket), "prod", WithAcknowledged(true))
	if connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatalf("an agent's change to prod with nothing installed: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("it ran")
	}
}

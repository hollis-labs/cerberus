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

// A pipeline's stages call the connector directly, so an agent could route
// a change to production through any pipeline the operator defined. The
// run's target now carries the labels of what its stages change (B2): an
// agent's run of a pipeline that starts a production resource is refused,
// or asked for approval, and nothing starts; a person's run goes ahead; an
// agent's run of a pipeline that only touches a dev resource is not
// covered.
func TestAnAgentsPipelineRunOnProdIsCoveredByTheBuiltInProtections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withPDP(t, policy.BaselineOnly(policy.SnapshotBaseline))
	SetEnforcement(SnapshotEnforcement{})
	t.Cleanup(func() { SetEnforcement(nil) })
	dir := t.TempDir()
	marker := func(id string) string { return filepath.Join(dir, id) }
	proc := func(id string, env target.Env, admin string) config.ResourceDef {
		return config.ResourceDef{ID: id, Type: "process", Connector: "local", Env: env, Owner: target.OwnerSelf, Admin: target.Admin{Default: admin},
			Config: map[string]any{"command": []string{"/usr/bin/touch", marker(id)}}}
	}
	starts := func(pid, rid string) config.PipelineDef {
		return config.PipelineDef{ID: pid, Name: pid, Stages: []config.StageDef{{Name: "one", Actions: []config.ActionDef{{Type: "start", Resource: rid}}}}}
	}
	cfg := &config.ConfigV2{
		Resources: []config.ResourceDef{proc("prod-api", target.EnvProd, target.AdminSelf), proc("owned", target.EnvDev, target.AdminOwner), proc("dev-api", target.EnvDev, target.AdminSelf)},
		Pipelines: []config.PipelineDef{starts("ship-prod", "prod-api"), starts("ship-owned", "owned"), starts("ship-dev", "dev-api")},
	}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	t.Cleanup(func() {
		for _, id := range []string{"prod-api", "owned", "dev-api"} {
			_, _ = svc.StopResource(context.Background(), id, WithAcknowledged(true))
		}
	})
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	_, err := svc.RunPipeline(agent, "ship-prod", WithAcknowledged(true))
	if code := connectorErrorCode(err); code != ExternalConnectorApprovalRequired && code != ExternalConnectorApprovalPending {
		t.Fatalf("an agent's run of a pipeline that starts a prod resource: %v", err)
	}
	if _, err = svc.RunPipeline(agent, "ship-owned", WithAcknowledged(true)); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatalf("an agent's run of a pipeline that starts an owner-administered resource: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, id := range []string{"prod-api", "owned"} {
		if _, statErr := os.Stat(marker(id)); statErr == nil {
			t.Fatalf("%s started", id)
		}
	}
	if out, err := svc.RunPipeline(agent, "ship-dev", WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("an agent's run of a dev pipeline: %+v %v", out, err)
	}
	if out, err := svc.RunPipeline(callerAs(confirmHuman, SurfaceSocket), "ship-prod", WithAcknowledged(true)); err != nil || !out.Success {
		t.Fatalf("a person's run: %+v %v", out, err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, statErr := os.Stat(marker("prod-api")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a person's run did not start the prod resource")
		}
	}
}

// Automation is the caller it acts for (B2): a pipeline started by an agent
// runs as automation acting for that agent, and that is covered; the
// monitor, and a person's run, are not.
func TestAutomationActingForAnAgentIsCovered(t *testing.T) {
	change := func(kind, actingFor string) policy.Request {
		return policy.Request{Connector: "local", Operation: "apply", Effect: "lifecycle", Principal: policy.Principal{Kind: kind, ActingFor: actingFor},
			Target: target.Target{Kind: "local.resource", ID: "api", Labels: target.Labels{Env: target.EnvProd}}}
	}
	for _, c := range []struct {
		kind, actingFor string
		want            bool
	}{
		{"automation", "agent", true},
		{"automation", "", false},
		{"automation", "human", false},
		{"automation", "something-else", true},
		{"human", "", false},
		{"agent", "", true},
	} {
		if got := policy.BaselineEnforced(change(c.kind, c.actingFor)); got != c.want {
			t.Errorf("%s acting for %q: enforced %v, want %v", c.kind, c.actingFor, got, c.want)
		}
	}
	p := pipelinePrincipal(callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket), "ship")
	if p.Kind != PrincipalAutomation || p.ActingFor != PrincipalAgent {
		t.Fatalf("a pipeline an agent started runs as %+v", p)
	}
}

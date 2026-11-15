package gate

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// The re-review's first-run repro (B2): on a default install — no policy
// applied, so shadow — an agent over MCP applied a resource labeled
// env: prod and admin: owner by setting acknowledged itself, and it ran,
// with the audit saying deny, would_block, shadow, allowed. The built-in
// protections are now enforced whatever the mode: the agent is refused.
// A person's call stays shadow, and an agent's change to a target with no
// such label is not enforced. (The old build drove the resource runtime;
// here the lane is a stand-in that runs nothing past the gate.)
func TestAnAgentsChangeToAProdOrOwnerTargetIsRefusedByDefault(t *testing.T) {
	withPDP(t, policy.BaselineOnly(policy.SnapshotBaseline))
	setEnforcement(SnapshotEnforcement{})
	t.Cleanup(func() { setEnforcement(nil) })
	res := func(id string, env target.Env, admin string) config.ResourceDef {
		return config.ResourceDef{ID: id, Type: "process", Connector: "local", Env: env, Owner: target.OwnerSelf, Admin: target.Admin{Default: admin},
			Config: map[string]any{"command": []string{"/bin/true"}}}
	}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{
		res("prod-owner", target.EnvProd, target.AdminOwner),
		res("prod-self", target.EnvProd, target.AdminSelf),
		res("dev-owner", target.EnvDev, target.AdminOwner),
		{ID: "unlabeled", Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/bin/true"}}},
	}}
	sink := audit.NewMemory()
	agent := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	for _, id := range []string{"prod-owner", "prod-self", "dev-owner"} {
		err := localLane(agent, sink, cfg, localconn.OpApply, id)
		code := connectorErrorCode(err)
		if code != CodePolicyDenied && code != CodeApprovalRequired && code != CodeApprovalPending {
			t.Errorf("%s: an agent's apply was not refused: %v", id, err)
		}
	}
	// The admin: owner repro is a deny, not an approval to ask for.
	if err := localLane(agent, sink, cfg, localconn.OpApply, "prod-owner"); connectorErrorCode(err) != CodePolicyDenied {
		t.Errorf("prod-owner: %v", err)
	}
	// Outside the built-in protections, shadow is unchanged.
	if err := localLane(agent, sink, cfg, localconn.OpApply, "unlabeled"); err != nil {
		t.Fatalf("an agent's apply of an unlabeled resource: %v", err)
	}
	person := callerAs(confirmHuman, SurfaceSocket)
	if err := localLane(person, sink, cfg, localconn.OpApply, "prod-owner"); err != nil {
		t.Fatalf("a person's apply stays shadow: %v", err)
	}
}

// The built-in protections are held by the gate itself, whatever policy
// decision point or enforcement is installed: with none installed, a deny
// on an agent's change to a prod target is still applied.
func TestTheGateHoldsTheBuiltInProtectionsItself(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Deny})
	setEnforcement(nil)
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "prod", Type: "process", Connector: "local", Env: target.EnvProd,
		Config: map[string]any{"command": []string{"/bin/true"}}}}}
	err := localLane(callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket), audit.NewMemory(), cfg, localconn.OpApply, "prod")
	if connectorErrorCode(err) != CodePolicyDenied {
		t.Fatalf("an agent's change to prod with nothing installed: %v", err)
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
	p := PipelinePrincipal(callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket), "ship")
	if p.Kind != PrincipalAutomation || p.ActingFor != PrincipalAgent {
		t.Fatalf("a pipeline an agent started runs as %+v", p)
	}
}

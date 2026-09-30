package cerbapi

import (
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func previewSpec(pluginServed bool) auditSpec {
	spec := auditSpec{connector: "cloudflare", operation: "set_dns_record_set", dryRun: true, known: true,
		op: contract.Operation{Name: "set_dns_record_set", Effect: contract.EffectDestructive}}
	if pluginServed {
		spec.preview = audit.PreviewPluginClaimed
	}
	return spec
}

// A plugin's preview runs the plugin's code with its credentials, so a
// lockdown and a freeze stop it, as the read_sensitive call it is; a host
// preview runs nothing and passes (M8).
func TestAPluginPreviewIsBraked(t *testing.T) {
	store := withBrakes(t)
	ctx := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	if _, _, err := EngageLockdown(ctx, audit.NewMemory(), store, "incident"); err != nil {
		t.Fatal(err)
	}
	if err := brakeRefusal(ctx, previewSpec(true), target.Target{}, true); connectorErrorCode(err) != ExternalConnectorLockdown {
		t.Fatalf("a plugin preview under lockdown: %v", err)
	}
	if err := brakeRefusal(ctx, previewSpec(false), target.Target{}, true); err != nil {
		t.Fatalf("a host preview under lockdown: %v", err)
	}
}

// A plugin's preview counts against the operation's rate, in its own
// read_sensitive bucket; a host preview is not counted.
func TestAPluginPreviewIsRated(t *testing.T) {
	res := policy.Result{Matched: []policy.Match{{Rule: "agents.dns", Rate: &policy.Rate{Limit: 5, Window: time.Minute}}}}
	ctx := callerAs(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}, SurfaceSocket)
	holds := rateHolds(ctx, previewSpec(true), res)
	if len(holds) != 1 || holds[0].key != rateKey("agents.dns", principalFor(ctx, previewSpec(true)), string(contract.EffectReadSensitive)) {
		t.Fatalf("plugin preview holds %+v", holds)
	}
	if holds := rateHolds(ctx, previewSpec(false), res); len(holds) != 0 {
		t.Fatalf("host preview holds %+v", holds)
	}
}

// Egress shapes a plugin's preview, which the plugin composed, and every
// real run; a host preview is Cerberus's own words.
func TestEgressShapesAPluginPreview(t *testing.T) {
	if !egressApplies(previewSpec(true)) || egressApplies(previewSpec(false)) || !egressApplies(auditSpec{}) {
		t.Fatal("egress applies to the wrong calls")
	}
}

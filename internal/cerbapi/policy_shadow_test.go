package cerbapi

import (
	"context"
	"reflect"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// denyAll is a decision point that would refuse everything.
type denyAll struct{}

func (denyAll) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: policy.Deny, WouldBlock: true, Snapshot: "test-deny-all",
		Matched: []policy.Match{{Rule: "test.deny-all", Decision: policy.Deny, Reason: "denies everything"}}}
}

func withPDP(t *testing.T, pdp policy.PDP) {
	t.Helper()
	SetPolicyDecisionPoint(pdp)
	t.Cleanup(func() { SetPolicyDecisionPoint(nil) })
}

type shadowRun struct {
	results []any
	errs    []string
	outcome []string
}

// runShadowScenario drives every gated lane once and returns what callers
// saw and how each outcome was coded.
func runShadowScenario(t *testing.T) (shadowRun, []audit.Record) {
	t.Helper()
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "notes-api", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
			Config: map[string]any{"command": []string{"/bin/true"}}},
	}}
	sink := audit.NewMemory()
	svc := auditedDockerService(sink)
	svc.SetResourceLookup(ConfigResourceLookup(cfg))
	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	var run shadowRun
	record := func(res any, err error) {
		run.results = append(run.results, res)
		if err != nil {
			run.errs = append(run.errs, err.Error())
		} else {
			run.errs = append(run.errs, "")
		}
	}
	record(svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}))
	record(svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}}))
	record(runtime.StopResource(ctx, "notes-api"))
	record(runtime.RunPipeline(ctx, "missing", WithAcknowledged(true)))
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindOutcome {
			run.outcome = append(run.outcome, rec.Decision+":"+rec.OutcomeCode)
		}
	}
	return run, sink.Records()
}

// Shadow mode: a decision point that would refuse everything changes no
// result, no error and no outcome code. It only adds the decision to the
// record.
func TestShadowPolicyNeverChangesAnOutcome(t *testing.T) {
	withPDP(t, nil)
	baseline, _ := runShadowScenario(t)
	withPDP(t, denyAll{})
	shadowed, recs := runShadowScenario(t)
	if !reflect.DeepEqual(baseline.errs, shadowed.errs) || !reflect.DeepEqual(baseline.outcome, shadowed.outcome) {
		t.Fatalf("shadow policy changed an outcome:\n baseline %v %v\n deny-all %v %v", baseline.errs, baseline.outcome, shadowed.errs, shadowed.outcome)
	}
	if !reflect.DeepEqual(baseline.results, shadowed.results) {
		t.Fatalf("shadow policy changed a result")
	}
	if baseline.errs[0] != "" {
		t.Fatalf("the read failed under the baseline: %s", baseline.errs[0])
	}
	for _, rec := range recs {
		if rec.Policy == nil || rec.Policy.Decision != "deny" || !rec.Policy.WouldBlock || !rec.Policy.Shadow || rec.Policy.Snapshot != "test-deny-all" {
			t.Fatalf("%s %s %s: policy %+v", rec.Kind, rec.Connector, rec.Operation, rec.Policy)
		}
	}
}

// The recorded decision is the real evaluator's, with every matched rule:
// a human's lifecycle on a local dev resource is allowed (Decision 11), an
// agent's is not.
func TestRecordedDecisionNamesItsRules(t *testing.T) {
	withPDP(t, nil)
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "notes-api", Type: "process", Connector: "local",
		Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{"command": []string{"/bin/true"}}}}}
	for kind, want := range map[PrincipalKind]struct {
		decision string
		rule     string
		block    bool
	}{
		PrincipalHuman: {"allow", "builtin.local-dev-lifecycle", false},
		PrincipalAgent: {"approve", "baseline.lifecycle.agent", true},
	} {
		sink := audit.NewMemory()
		runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
		ctx := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: kind, Via: ViaCLI}), SurfaceInProcess)
		_, _ = runtime.StopResource(ctx, "notes-api")
		intent := sink.Records()[0]
		p := intent.Policy
		if p == nil || p.Decision != want.decision || p.WouldBlock != want.block || p.Snapshot != policy.SnapshotBaseline || len(p.MatchedRules) == 0 || p.MatchedRules[0].Rule != want.rule {
			t.Fatalf("%s: %+v", kind, p)
		}
	}
}

// The monitor is not an operation request: it is not authorized.
func TestMonitorRestartIsNotAuthorized(t *testing.T) {
	sink := audit.NewMemory()
	call, _ := beginAudit(context.Background(), sink, nil, auditSpec{connector: "local", operation: "apply", automation: true})
	call.finish(nil)
	for _, rec := range sink.Records() {
		if rec.Policy != nil {
			t.Fatalf("automation authorized: %+v", rec.Policy)
		}
	}
}

func TestPolicyLoadMismatchIsRecorded(t *testing.T) {
	sink := audit.NewMemory()
	if err := RecordPolicyLoad(sink, policy.LoadStatus{Snapshot: policy.SnapshotBaseline}); err != nil || len(sink.Records()) != 0 {
		t.Fatal("a clean load was recorded")
	}
	if err := RecordPolicyLoad(sink, policy.LoadStatus{Snapshot: policy.SnapshotMismatch, Recorded: "sha256:a", Found: "sha256:b", Problem: "changed"}); err != nil {
		t.Fatal(err)
	}
	recs := sink.Records()
	if len(recs) != 2 || recs[1].OutcomeCode != PolicySnapshotChanged || recs[1].Target.Fields["found"] != "sha256:b" || recs[0].Principal.Kind != audit.PrincipalAutomation {
		t.Fatalf("records %+v", recs)
	}
}

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
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func dockerLane(t *testing.T, sink audit.Sink) (*dockerService, *fakeDockerBackend) {
	t.Helper()
	backend := &fakeDockerBackend{}
	svc := auditedDockerServiceWith(sink, backend)
	svc.SetResourceLookup(ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "dev-box", Type: "container", Connector: "docker", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
	}}))
	return svc, backend
}

// Where enforcement is on, a deny refuses before the connector runs, with
// the reason and the rule, and the refusal is the call's recorded outcome.
func TestEnforcedDenyRefusesBeforeAnythingRuns(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Deny})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	_, err := svc.Execute(BeginRequest(context.Background(), SurfaceInProcess), laneArgs{Connector: "docker", Operation: "list_containers"})
	if connectorErrorCode(err) != CodePolicyDenied || !strings.Contains(err.Error(), "because the test says so") || !strings.Contains(err.Error(), "rule test.rule") {
		t.Fatalf("err = %v", err)
	}
	if backend.lists != 0 {
		t.Fatal("the connector ran under a deny")
	}
	if o := outcome(sink.Records()); o.Decision != audit.DecisionRefused || o.OutcomeCode != string(CodePolicyDenied) {
		t.Fatalf("outcome %+v", o)
	}
}

// An approve decision asks the daemon's broker for an approval and answers
// approval_pending with its id, expiry and the command that decides it —
// recorded as approval_requested, linked to the call's intent.
func TestEnforcedApproveRequestsAnApproval(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, backend := dockerLane(t, sink)
	ctx := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2"}), SurfaceSocket)
	_, err = svc.Execute(ctx, laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil {
		t.Fatalf("err = %v", err)
	}
	if backend.stopFile != "" || backend.started != "" {
		t.Fatal("the connector ran while approval was pending")
	}
	a, ok := broker.Get(coded.Approval.ID)
	if !ok || a.Status != approval.Pending || a.Rule != "test.rule" || a.Channel != approval.ChannelTTYConfirm || a.Principal.Kind != "agent" || a.Target.Resource != "dev-box" || a.ArgsDigest == "" {
		t.Fatalf("approval %+v", a)
	}
	if !strings.Contains(err.Error(), a.ApproveWith()) || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("the refusal does not name how to decide it: %v", err)
	}
	var intent, requested audit.Record
	for _, rec := range sink.Records() {
		switch rec.Kind {
		case audit.KindIntent:
			intent = rec
		case audit.KindApprovalRequested:
			requested = rec
		}
	}
	if requested.Approval == nil || requested.Approval.ID != a.ID || requested.OperationID != intent.OperationID || a.RequestOperationID != intent.OperationID {
		t.Fatalf("requested %+v intent %s", requested, intent.OperationID)
	}
	if o := outcome(sink.Records()); o.OutcomeCode != string(CodeApprovalPending) {
		t.Fatalf("outcome %+v", o)
	}
}

// A prod, shared or not-ours target needs out of band (Decision 3); with no
// broker — the in-process CLI without a daemon — that cannot be asked for,
// and the answer says how to start one (D3). A tty_confirm is asked for on
// the terminal (P3-3).
func TestApprovalWithoutABroker(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	svc, _ := dockerLane(t, sink)
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	_, err := svc.Execute(ctx, laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true})
	if connectorErrorCode(err) != CodeApprovalRequired || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("dev target: %v", err)
	}
	_, err = svc.Execute(ctx, laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true})
	if connectorErrorCode(err) != CodeApprovalPending || !strings.Contains(err.Error(), "cerberus daemon") {
		t.Fatalf("unlabeled target: %v", err)
	}
}

// Allow runs; an approve on a dry run runs (the plan step); automation is
// never enforced.
func TestEnforcedAllowAndDryRunRun(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	withEnforcement(t, nil)
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	if _, err := svc.Execute(BeginRequest(context.Background(), SurfaceInProcess), laneArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("allow: %v", err)
	}
	withPDP(t, constantPDP{decision: policy.Approve})
	call, err := beginGated(context.Background(), sink, nil, Spec{Connector: "local", Operation: "apply", Automation: true})
	if err != nil || call == nil {
		t.Fatalf("automation was enforced: %v", err)
	}
}

func TestApprovalTermsReadTheTarget(t *testing.T) {
	approve := policy.Result{Decision: policy.Approve, Matched: []policy.Match{{Rule: "r", Decision: policy.Approve, Approval: &policy.Approval{Scope: "session", TTL: 10 * time.Minute}}}}
	self := tgt(target.EnvDev, target.OwnerSelf, target.AdminSelf)
	if c, s, ttl := approvalTerms(self, approve); c != approval.ChannelTTYConfirm || s != "session" || ttl != 10*time.Minute {
		t.Fatalf("dev self: %s %s %s", c, s, ttl)
	}
	for name, tgt := range map[string]target.Target{
		"prod":     tgt(target.EnvProd, target.OwnerSelf, target.AdminSelf),
		"unknown":  tgt(target.EnvUnknown, target.OwnerUnknown, target.AdminUnknown),
		"shared":   tgt(target.EnvWork, target.OwnerSelf, target.AdminShared),
		"not ours": tgt(target.EnvWork, "platform-team", target.AdminSelf),
	} {
		if c, _, _ := approvalTerms(tgt, approve); c != approval.ChannelOutOfBand {
			t.Errorf("%s: %s", name, c)
		}
	}
}

// Every broker transition is an audit record of its own kind.
func TestBrokerRecordsEveryTransition(t *testing.T) {
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	intent := audit.Record{OperationID: "op-1", Connector: "local", Operation: "remove", ArgsDigest: "hmac:x", Principal: audit.Principal{Kind: "agent"}}
	res := constantPDP{decision: policy.Approve}.Authorize(policy.Request{})
	a, err := broker.Request(ctx, intent, res, approval.ChannelTTYConfirm, approval.ScopeOnce, time.Hour, PlanSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Decide(ctx, a.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Consume(ctx, a.ID, approval.ConsumeCheck{Principal: audit.Principal{Kind: "agent"}, Connector: "local", Operation: "remove", ArgsDigest: "hmac:x", OperationID: "op-2"}); err != nil {
		t.Fatal(err)
	}
	b, _ := broker.Request(ctx, intent, res, approval.ChannelTTYConfirm, approval.ScopeOnce, time.Hour, PlanSnapshot{})
	_, _ = broker.Decide(ctx, b.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human"}})
	_, _ = broker.Revoke(ctx, b.ID, approval.Decision{By: audit.Principal{Kind: "human"}})
	var kinds []string
	for _, rec := range sink.Records() {
		kinds = append(kinds, rec.Kind)
	}
	want := "approval_requested approval_decided approval_consumed approval_requested approval_decided approval_revoked"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("kinds %v", kinds)
	}
	consumed := sink.Records()[2]
	if consumed.OperationID != "op-2" || consumed.Approval.Status != "consumed" || consumed.Approval.DecidedBy == nil {
		t.Fatalf("consumed %+v", consumed)
	}
	// An out-of-band approval never consumes until P3-4 verifies presence.
	oob, _ := broker.Request(ctx, intent, res, approval.ChannelOutOfBand, approval.ScopeOnce, time.Hour, PlanSnapshot{})
	if _, err := broker.Decide(ctx, oob.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human"}}); !errors.Is(err, approval.ErrNoPresence) {
		t.Fatalf("out of band without presence: %v", err)
	}
}

func tgt(env target.Env, owner, admin string) target.Target {
	return target.Target{Labels: target.Labels{Env: env, Owner: owner}, AdminFor: admin}
}

// Shadow mode is unchanged: with a broker installed and a decision point
// that wants approval for everything, but enforcement off, the operation
// runs and no approval is asked for.
func TestShadowAsksForNoApproval(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	setBroker(broker)
	t.Cleanup(func() { setBroker(nil) })
	svc, backend := dockerLane(t, sink)
	if _, err := svc.Execute(BeginRequest(context.Background(), SurfaceInProcess), laneArgs{Connector: "docker", Operation: "list_containers"}); err != nil || backend.lists != 1 {
		t.Fatalf("shadow: %v", err)
	}
	if n := len(broker.List()); n != 0 {
		t.Fatalf("shadow asked for %d approval(s)", n)
	}
}

// approveNext asks for an approval of the call, decides it approved, and
// returns its id.
func approveNext(ctx context.Context, t *testing.T, svc *dockerService, broker *Broker, args laneArgs) approval.Approval {
	t.Helper()
	_, err := svc.Execute(ctx, args)
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeApprovalPending || coded.Approval == nil {
		t.Fatalf("asking: %v", err)
	}
	a, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A retry naming an approved approval runs once, and its outcome says which
// approval it ran under and which plan. The approval is spent: naming it
// again asks again.
func TestApprovalIsUsedByID(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, backend := dockerLane(t, sink)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	args := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
	a := approveNext(ctx, t, svc, broker, args)
	if !strings.HasPrefix(a.PlanHash, "sha256:") {
		t.Fatalf("the approval binds to no plan: %+v", a)
	}
	args.ApprovalID = a.ID
	if _, err := svc.Execute(ctx, args); err != nil {
		t.Fatalf("approved call: %v", err)
	}
	if backend.stopped != "web" {
		t.Fatal("the approved call did not run")
	}
	if o := outcome(sink.Records()); o.ApprovalID != a.ID || o.PlanHash != a.PlanHash || o.Decision == audit.DecisionRefused {
		t.Fatalf("outcome %+v", o)
	}
	if got, _ := broker.Get(a.ID); got.Status != approval.Consumed {
		t.Fatalf("status %s", got.Status)
	}
	backend.stopped = ""
	if _, err := svc.Execute(ctx, args); connectorErrorCode(err) != CodeApprovalRequired || !strings.Contains(err.Error(), "ask again") || backend.stopped != "" {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := svc.Execute(ctx, laneArgs{Connector: "docker", Operation: "stop", Config: args.Config, Acknowledged: true, ApprovalID: "apr_missing"}); connectorErrorCode(err) != CodeApprovalRequired {
		t.Fatalf("unknown id: %v", err)
	}
}

// An approval covers the plan it was given for. A changed argument, a
// relabeled target or another operation is plan_stale, and nothing runs.
func TestChangedCallIsPlanStale(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	base := func() laneArgs {
		return laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
	}
	for name, change := range map[string]func(*dockerService, *laneArgs){
		"argument": func(_ *dockerService, args *laneArgs) {
			args.Config["container"] = "api"
		},
		"target label": func(svc *dockerService, _ *laneArgs) {
			svc.SetResourceLookup(ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
				{ID: "dev-box", Type: "container", Connector: "docker", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}, Tags: []string{"shared"}},
			}}))
		},
		"operation": func(_ *dockerService, args *laneArgs) {
			args.Operation = "start"
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, backend := dockerLane(t, sink)
			args := base()
			a := approveNext(ctx, t, svc, broker, args)
			args.ApprovalID = a.ID
			change(svc, &args)
			_, err := svc.Execute(ctx, args)
			if connectorErrorCode(err) != CodePlanStale || !strings.Contains(err.Error(), a.ID) {
				t.Fatalf("err = %v", err)
			}
			if backend.stopped != "" || backend.started != "" {
				t.Fatal("a stale approval ran")
			}
			if got, _ := broker.Get(a.ID); got.Status != approval.Approved {
				t.Fatalf("a refused use spent the approval: %s", got.Status)
			}
		})
	}
}

// The gate recomputes the lane's plan when the approval is used; a preview
// or a plugin binary that changed in between is plan_stale. The spec's plan
// function stands in for a plugin lane here.
func TestChangedPreviewOrPluginIsPlanStale(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	resources := ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "dev-box", Type: "container", Connector: "demo", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
	}})
	for name, change := range map[string]func(*plan.Plan){
		"preview":       func(p *plan.Plan) { _ = p.WithPreview("plugin", map[string]any{"etag": "v2"}) },
		"plugin sha":    func(p *plan.Plan) { p.PluginEntrypointSHA256 = "sha256:other" },
		"plugin config": func(p *plan.Plan) { p.PluginConfigSHA256 = "sha256:other" },
	} {
		t.Run(name, func(t *testing.T) {
			current := plan.Plan{Lane: plan.LaneAdmin, Connector: "demo", Operation: "write", PluginEntrypointSHA256: "sha256:bin"}
			_ = current.WithPreview("plugin", map[string]any{"etag": "v1"})
			spec := Spec{Connector: "demo", Operation: "write", Known: true, Op: contract.Operation{Name: "write", Effect: contract.EffectWrite},
				Config: map[string]any{"resource": "dev-box"}, Acknowledged: true, Resources: resources,
				Plan: func(context.Context) (plan.Plan, error) { return current, nil }}
			_, err := beginGated(ctx, sink, nil, spec)
			var coded *Error
			if !errors.As(err, &coded) || coded.Code != CodeApprovalPending {
				t.Fatalf("asking: %v", err)
			}
			if _, derr := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human"}}); derr != nil {
				t.Fatal(derr)
			}
			spec.ApprovalID = coded.Approval.ID
			change(&current)
			if _, gerr := beginGated(ctx, sink, nil, spec); connectorErrorCode(gerr) != CodePlanStale {
				t.Fatalf("changed %s: %v", name, gerr)
			}
			current = plan.Plan{Lane: plan.LaneAdmin, Connector: "demo", Operation: "write", PluginEntrypointSHA256: "sha256:bin"}
			_ = current.WithPreview("plugin", map[string]any{"etag": "v1"})
			call, err := beginGated(ctx, sink, nil, spec)
			if err != nil {
				t.Fatalf("unchanged plan: %v", err)
			}
			if call.intent.ApprovalID != coded.Approval.ID || call.intent.PlanHash == "" {
				t.Fatalf("intent %+v", call.intent)
			}
		})
	}
}

// A plan asked for on its own is the one an approval of the same call binds
// to, runs nothing, and is recorded as a dry run.
func TestPlanOnRequestIsTheApprovedPlan(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, backend := dockerLane(t, sink)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	args := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
	shown := args
	shown.Plan = true
	res, err := svc.Execute(ctx, shown)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := res.(ConnectorPlan)
	if !ok || p.Plan.Target.Resource != "dev-box" || p.Plan.ArgsDigest == "" || p.Plan.V != plan.Version {
		t.Fatalf("plan %#v", res)
	}
	if backend.stopped != "" {
		t.Fatal("asking for a plan ran the operation")
	}
	if o := outcome(sink.Records()); !o.DryRun {
		t.Fatalf("a plan request was not recorded as a dry run: %+v", o)
	}
	if a := approveNext(ctx, t, svc, broker, args); a.PlanHash != p.PlanHash {
		t.Fatalf("approval binds %s, plan shown %s", a.PlanHash, p.PlanHash)
	}
}

// A once approval is the requester's: another kind, another via or another
// session cannot spend it, and it stays approved for the one that asked.
func TestApprovalBelongsToItsRequester(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, backend := dockerLane(t, sink)
	asCaller := func(p Principal) context.Context {
		return BeginRequest(WithPrincipal(context.Background(), p), SurfaceSocket)
	}
	asker := Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2", Session: "s1"}
	args := laneArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true}
	a := approveNext(asCaller(asker), t, svc, broker, args)
	args.ApprovalID = a.ID
	others := map[string]Principal{
		"another session": {Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2", Session: "s2"},
		"another via":     {Kind: PrincipalAgent, Via: ViaMCPHTTP, Session: "s1"},
		"another kind":    {Kind: PrincipalHuman, Via: ViaMCPStdio, Session: "s1"},
	}
	for name, p := range others {
		_, err := svc.Execute(asCaller(p), args)
		if connectorErrorCode(err) != CodeApprovalRequired || !strings.Contains(err.Error(), "belongs to another caller") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if backend.stopped != "" {
		t.Fatal("another caller spent the approval")
	}
	if _, err := svc.Execute(asCaller(asker), args); err != nil || backend.stopped != "web" {
		t.Fatalf("the requester: %v", err)
	}
}

// Every consume refusal carries its recovery, and redaction leaves it whole.
func TestConsumeRefusalsSurviveRedaction(t *testing.T) {
	args := opArgs{Connector: "docker", Operation: "stop"}
	a := approval.Approval{ID: "apr_0123456789ab", Status: approval.Denied, Connector: "docker", Operation: "start",
		Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio"}, ExpiresAt: time.Now().Add(time.Hour)}
	for _, cause := range []error{approval.ErrExpired, approval.ErrPlanStale, approval.ErrArgsMismatch, approval.ErrOtherOperation,
		approval.ErrOtherPrincipal, approval.ErrNotApproved, approval.ErrNotFound, approval.ErrNoPresence} {
		msg := consumeRefusal(args, a.ID, a, cause).Error()
		if got := redact.Text(msg); got != msg {
			t.Errorf("%v: redaction changed the refusal:\n  %s\n  %s", cause, msg, got)
		}
		if !strings.Contains(msg, a.ID) {
			t.Errorf("%v: the refusal does not name the approval: %s", cause, msg)
		}
	}
}

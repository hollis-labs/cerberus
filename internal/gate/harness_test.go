package gate

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The old build's tests installed process globals (SetPolicyDecisionPoint,
// SetEnforcement, SetBroker, SetBrakes, SetRateLimiter, SetNotifier) and
// called package functions. The gate has no globals: these helpers keep the
// tests' shape by holding what each test installs and building a Gate from
// it with New, so every ported test runs through Gate.Begin.

type testEnv struct {
	pdp         policy.PDP
	enforcement Enforcement
	broker      *Broker
	brakes      *Brakes
	rates       *RateLimiter
	notify      func(title, message string)
	inProcess   func() error
	now         func() time.Time

	sink audit.Sink
	g    *Gate
}

var env testEnv

func (e *testEnv) invalidate() { e.g, e.sink = nil, nil }

// gateFor is the gate the current test installed, recording to sink. It is
// rebuilt when what the test installed changes, and kept otherwise, so the
// breaker's counts carry from call to call as they did in a process.
func gateFor(sink audit.Sink) *Gate {
	if env.g != nil && env.sink == sink {
		return env.g
	}
	if env.rates != nil {
		env.rates.PDP = nil
	}
	env.g = New(Options{Sink: sink, Policy: env.pdp, Enforcement: env.enforcement, Broker: env.broker, Brakes: env.brakes,
		Rates: env.rates, Notify: env.notify, InProcessStateCheck: env.inProcess, Now: env.now})
	env.sink = sink
	return env.g
}

func withPDP(t *testing.T, pdp policy.PDP) {
	t.Helper()
	env.pdp = pdp
	env.invalidate()
	t.Cleanup(func() { env.pdp = nil; env.invalidate() })
}

func setEnforcement(e Enforcement) { env.enforcement = e; env.invalidate() }
func setBroker(b *Broker)          { env.broker = b; env.invalidate() }
func setBrakes(b *Brakes)          { env.brakes = b; env.invalidate() }
func setRateLimiter(l *RateLimiter) {
	env.rates = l
	env.invalidate()
}
func setNotifier(n func(title, message string)) { env.notify = n; env.invalidate() }
func setInProcessStateCheck(c func() error)     { env.inProcess = c; env.invalidate() }

// beginGated is Gate.Begin on the gate the test installed.
func beginGated(ctx context.Context, sink audit.Sink, _ *slog.Logger, spec Spec) (*Call, error) {
	return gateFor(sink).Begin(ctx, spec)
}

// beginAudit is the intent record alone, as the old build's beginAudit.
func beginAudit(ctx context.Context, sink audit.Sink, _ *slog.Logger, spec Spec) (*Call, error) {
	return gateFor(sink).beginAudit(ctx, spec)
}

func connectorErrorCode(err error) ErrorCode { return CodeOf(err) }

// enforceAll is an Enforcement that enforces everything, for tests.
type enforceAll struct{}

func (enforceAll) Enforced(policy.Request) bool { return true }

func withEnforcement(t *testing.T, broker *Broker) {
	t.Helper()
	setEnforcement(enforceAll{})
	setBroker(broker)
	t.Cleanup(func() { setEnforcement(nil); setBroker(nil) })
}

// constantPDP decides every request the same way.
type constantPDP struct {
	decision policy.Decision
	approval *policy.Approval
}

func (constantPDP) GlobalPosture() string { return policy.PostureSecure }

func (c constantPDP) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: c.decision, WouldBlock: c.decision != policy.Allow, Snapshot: "test",
		Matched: []policy.Match{{Rule: "test.rule", Decision: c.decision, Reason: "because the test says so", Approval: c.approval}}}
}

// denyAll is a decision point that would refuse everything.
type denyAll struct{}

func (denyAll) GlobalPosture() string { return policy.PostureSecure }

func (denyAll) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: policy.Deny, WouldBlock: true, Snapshot: "test-deny-all",
		Matched: []policy.Match{{Rule: "test.deny-all", Decision: policy.Deny, Reason: "denies everything"}}}
}

// laneArgs is a call as a lane receives it (the old build's
// ExternalConnectorOperationArgs).
type laneArgs struct {
	Connector         string
	Operation         string
	Config            map[string]any
	DryRun            bool
	Acknowledged      bool
	ApprovalID        string
	Plan              bool
	ConfirmedPlanHash string
	BreakGlass        *BreakGlassRequest
}

// fakeDockerBackend records what a call would have done to docker.
type fakeDockerBackend struct {
	started  string
	stopped  string
	stopFile string
	lists    int
	logName  string
	logs     string
}

// dockerService is a lane in front of the gate, standing in for the old
// build's admin lane on the docker connector: it checks the contract,
// begins the gated call with the lane's plan function, runs the fake
// backend, shapes the result and finishes.
type dockerService struct {
	sink      audit.Sink
	backend   *fakeDockerBackend
	resources ResourceLookup
	def       contract.Definition
}

func auditedDockerServiceWith(sink audit.Sink, backend *fakeDockerBackend) *dockerService {
	return &dockerService{sink: sink, backend: backend, def: dockerconn.Definition()}
}

func (s *dockerService) SetResourceLookup(lookup ResourceLookup) { s.resources = lookup }

func (s *dockerService) spec(args laneArgs) Spec {
	op, known := s.def.Operation(args.Operation)
	spec := Spec{Connector: args.Connector, Operation: args.Operation, Op: op, Known: known, Config: args.Config,
		Acknowledged: args.Acknowledged, DryRun: args.DryRun, ApprovalID: args.ApprovalID, PlanOnly: args.Plan,
		ConfirmedPlanHash: args.ConfirmedPlanHash, BreakGlass: args.BreakGlass, Resources: s.resources}
	planSpec := spec
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := AuditTarget(planSpec)
		return plan.Plan{Lane: plan.LaneAdmin, Connector: args.Connector, Operation: args.Operation, Effect: string(op.Effect),
			Target: tgt, ArgsDigest: s.sink.Digest(args.Config)}, nil
	}
	return spec
}

// contractRefusal is the old admin lane's contract gate: a declared
// operation, its key table, the acknowledgment, and a preview for a dry run.
func (s *dockerService) contractRefusal(ctx context.Context, args laneArgs, spec Spec) error {
	if !spec.Known {
		return NewError(args.Connector, args.Operation, CodeUnsupported, redact.Guidance("%s does not declare operation %q; refusing", args.Connector, args.Operation))
	}
	if err := spec.Op.CheckInputs(args.Config, CallerSurfaceFrom(ctx) == SurfaceInProcess); err != nil {
		return NewError(args.Connector, args.Operation, CodeInvalidArgs, redact.Prose(err))
	}
	if args.Plan {
		return nil
	}
	if args.DryRun && spec.Op.Preview == contract.PreviewNone {
		return NewError(args.Connector, args.Operation, CodePreviewUnsupported, redact.Guidance("%s %s has no dry run", args.Connector, args.Operation))
	}
	if spec.Op.RequiresAck && !args.Acknowledged {
		return NewError(args.Connector, args.Operation, CodeAckRequired, errors.New("requires operator acknowledgment"))
	}
	return nil
}

// Plan is the lane's plan for args, as an approval binds it.
func (s *dockerService) Plan(ctx context.Context, args laneArgs) (plan.Plan, error) {
	return s.spec(args).Plan(ctx)
}

func (s *dockerService) Execute(ctx context.Context, args laneArgs) (any, error) {
	if args.Plan {
		args.DryRun, args.ApprovalID, args.ConfirmedPlanHash, args.BreakGlass = true, "", "", nil
	}
	spec := s.spec(args)
	call, err := gateFor(s.sink).Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	// The lane's own contract gate, after the gate began the call, so every
	// refusal is the call's recorded outcome (the old admin lane's order).
	if refusal := s.contractRefusal(ctx, args, spec); refusal != nil {
		call.Finish(refusal)
		return nil, refusal
	}
	if args.Plan {
		p, perr := call.ShowPlan(ctx)
		call.Finish(perr)
		if perr != nil {
			return nil, perr
		}
		return *p, nil
	}
	var result any
	switch args.Operation {
	case "list_containers":
		s.backend.lists++
		result = []map[string]any{{"id": "abc", "name": "web", "state": "running"}}
	case "start":
		s.backend.started, _ = args.Config["container"].(string)
		result = map[string]any{"started": s.backend.started}
	case "stop":
		s.backend.stopped, _ = args.Config["container"].(string)
		s.backend.stopFile, _ = args.Config["compose_file"].(string)
		result = map[string]any{"stopped": s.backend.stopped}
	case "logs":
		s.backend.logName, _ = args.Config["container"].(string)
		result = s.backend.logs
		if result == "" {
			result = "logs"
		}
	default:
		result = map[string]any{"ok": true}
	}
	if call.EgressApplies() {
		var err error
		if result, err = call.Shape(result); err != nil {
			call.Finish(err)
			return nil, err
		}
	}
	call.Finish(nil)
	return result, nil
}

func outcome(recs []audit.Record) audit.Record {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Kind == audit.KindOutcome {
			return recs[i]
		}
	}
	return audit.Record{}
}

// withBrakes installs brakes on a scratch store.
func withBrakes(t *testing.T) brake.Store {
	t.Helper()
	store := brake.Store{Dir: t.TempDir()}
	setBrakes(&Brakes{Store: store})
	t.Cleanup(func() { setBrakes(nil) })
	return store
}

// brakeGate is the installed gate with brakes on store recording to sink,
// for the old build's package-level brake functions.
func brakeGate(sink audit.Sink, store brake.Store) *Gate {
	if env.brakes == nil || env.brakes.Store.Dir != store.Dir {
		setBrakes(&Brakes{Store: store})
	}
	g := gateFor(sink)
	g.brakes.Sink = sink
	return g
}

func EngageLockdown(ctx context.Context, sink audit.Sink, store brake.Store, reason string) (brake.State, brake.Lockdown, error) {
	return brakeGate(sink, store).EngageLockdown(ctx, reason)
}

func EngageFreeze(ctx context.Context, sink audit.Sink, store brake.Store, match policy.TargetMatch, reason string) (brake.State, brake.Freeze, error) {
	return brakeGate(sink, store).EngageFreeze(ctx, match, reason)
}

func LiftLockdown(ctx context.Context, sink audit.Sink, store brake.Store, approvalID string) (brake.State, error) {
	return brakeGate(sink, store).LiftLockdown(ctx, approvalID)
}

func LiftFreeze(ctx context.Context, sink audit.Sink, store brake.Store, id, approvalID string) (brake.State, error) {
	return brakeGate(sink, store).LiftFreeze(ctx, id, approvalID)
}

func ResetSuspension(ctx context.Context, sink audit.Sink, store brake.Store, id, typed string) (brake.State, error) {
	return brakeGate(sink, store).ResetSuspension(ctx, id, typed, "")
}

// ownerConsole is the decider the HTTP layer establishes as the owner signed
// in to the console.
var ownerConsole = OwnerSession{ConsolePerson: true, SessionID: "sess_owner"}

// asOwner is ctx as the owner's console session: a human over the web.
func asOwner() context.Context {
	return WithPrincipal(BeginRequest(context.Background(), SurfaceWeb), WebSessionPrincipal("sess_owner"))
}

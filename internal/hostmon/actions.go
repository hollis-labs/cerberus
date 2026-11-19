package hostmon

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// The Outpost host actions, main's cerbapi host_actions.go moved: a unit operation on a host
// reached over Outpost (units, logs, start, stop, restart), gated as the host action contract's
// operation, planned on request, and run through the outbound pkg/outpost client with the token
// file read in place. Output passes the request's redaction scope and egress policy.

// ActionArgs are one action's request and its acknowledgment and approval.
type ActionArgs struct {
	Request      outpost.Request `json:"request"`
	Acknowledged bool            `json:"acknowledged,omitempty"`
	ApprovalID   string          `json:"approval_id,omitempty"`
}

func hostActionOperation(verb string) contract.Operation {
	effect := contract.EffectLifecycle
	if verb == "units" || verb == "logs" {
		effect = contract.EffectReadSensitive
	}
	return contract.Operation{Name: verb, Effect: effect, Reversible: true,
		Target:  contract.TargetDescriptor{Kind: "host.unit", From: []string{"name", "unit"}},
		Preview: contract.PreviewHost, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone}.Finalize()
}

// HostActionDefinition is the host action contract (main's).
func HostActionDefinition() contract.Definition {
	ops := []contract.Operation{}
	for _, verb := range []string{"units", "logs", "start", "stop", "restart"} {
		ops = append(ops, hostActionOperation(verb))
	}
	ops = append(ops, contract.Operation{Name: "plan", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "host.unit"},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone})
	return contract.Finalize(contract.Definition{ID: "host", Version: "builtin", Operations: ops})
}

// Actions runs the host actions through the gate.
type Actions struct {
	Prober    *Prober
	Gate      *gate.Gate
	Resources gate.ResourceLookup
}

// spec is main's actionSpec: the request checked, the host reached over Outpost, its target
// labels from a bound reach.ssh server resource, and the plan an approval binds to.
func (x *Actions) spec(name string, args ActionArgs, planning bool) (gate.Spec, hosts.Host, error) {
	if err := args.Request.Validate(); err != nil {
		return gate.Spec{}, hosts.Host{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if args.Request.Verb == "probe" {
		return gate.Spec{}, hosts.Host{}, fmt.Errorf("%w: use the host probe operation", ErrInvalid)
	}
	f, err := x.Prober.Load()
	if err != nil {
		return gate.Spec{}, hosts.Host{}, err
	}
	i := f.HostIndex(name)
	if i < 0 {
		return gate.Spec{}, hosts.Host{}, notFound("host", name)
	}
	h := f.Hosts[i]
	if h.Reach.EffectiveTransport() != "outpost" || h.Reach.Outpost == nil {
		return gate.Spec{}, h, fmt.Errorf("%w: %w", ErrInvalid, redact.Guidance("host %q needs reach.transport=outpost for unit operations", name))
	}
	op := hostActionOperation(args.Request.Verb)
	config := map[string]any{"name": name, "unit": args.Request.Unit, "lines": args.Request.Lines}
	var labels *target.ResourceLabels
	if h.Reach.SSH != "" && x.Resources != nil {
		if def, ok := x.Resources(h.Reach.SSH); ok && def != nil && def.Type == "server" && def.Connector == "ssh" {
			bound := def.TargetLabels()
			labels = &bound
		}
	}
	resolved := target.Resolve("host", op.Target.Kind, h.Name, labels, false)
	spec := gate.Spec{Connector: "host", Operation: args.Request.Verb, Op: op, Known: true, Config: config,
		Acknowledged: args.Acknowledged, ApprovalID: args.ApprovalID, PlanOnly: planning, DryRun: planning, ResolvedTarget: &resolved}
	digest := x.Gate.Sink().Digest
	planSpec := spec
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		p := plan.Plan{Lane: plan.LaneAdmin, Connector: "host", Operation: args.Request.Verb, Effect: string(op.Effect), Target: tgt,
			ArgsDigest: digest(config), Digests: map[string]string{"host": digest(h)}}
		if err := p.WithPreview(string(contract.PreviewHost), map[string]any{"host": name, "transport": "outpost", "request": args.Request}); err != nil {
			return p, err
		}
		return p, nil
	}
	return spec, h, nil
}

// Plan answers the plan an approval of the action would bind to; nothing runs.
func (x *Actions) Plan(ctx context.Context, name string, args ActionArgs) (_ *gate.ConnectorPlan, retErr error) {
	spec, _, err := x.spec(name, args, true)
	if err != nil {
		return nil, err
	}
	call, err := x.Gate.Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer func() { call.Finish(retErr) }()
	return call.ShowPlan(ctx)
}

// Execute runs the action on the host's Outpost. A non-zero exit is the operation failing.
func (x *Actions) Execute(ctx context.Context, name string, args ActionArgs) (_ *outpost.Result, retErr error) {
	spec, h, err := x.spec(name, args, false)
	if err != nil {
		return nil, err
	}
	ctx, scope := redact.EnsureScope(ctx)
	call, err := x.Gate.Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer func() { call.Finish(retErr) }()
	if spec.Op.RequiresAck && !args.Acknowledged {
		return nil, gate.NewError("host", args.Request.Verb, gate.CodeAckRequired,
			redact.Guidance("%s operation %q requires operator acknowledgment", spec.Op.Effect, args.Request.Verb))
	}
	client := outpost.Client{URL: h.Reach.Outpost.URL, TokenFile: h.Reach.Outpost.TokenFile, Credential: func(token string) { scope.Add("outpost/token", token) }}
	result, err := client.Execute(ctx, args.Request)
	if err != nil {
		return nil, errors.New(scope.ErrorText(err))
	}
	result.Output = scope.Text(result.Output)
	safe, err := gate.ShapeAs(call, &result)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return safe, gate.NewError("host", args.Request.Verb, gate.CodeOperationFailed, redact.Guidance("host unit operation exited with status %d", result.ExitCode))
	}
	return safe, nil
}

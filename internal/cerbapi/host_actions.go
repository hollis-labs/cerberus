package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

type HostActionArgs struct {
	Request      outpost.Request `json:"request"`
	Acknowledged bool            `json:"acknowledged,omitempty"`
	ApprovalID   string          `json:"approval_id,omitempty"`
}

type HostActionClient interface {
	PlanHostAction(context.Context, string, HostActionArgs) (*ConnectorPlan, error)
	ExecuteHostAction(context.Context, string, HostActionArgs) (outpost.Result, error)
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

func (s *HostService) actionSpec(name string, args HostActionArgs, planning bool) (auditSpec, hosts.Host, error) {
	if err := args.Request.Validate(); err != nil {
		return auditSpec{}, hosts.Host{}, &HostInvalidError{Err: err}
	}
	if args.Request.Verb == "probe" {
		return auditSpec{}, hosts.Host{}, &HostInvalidError{Err: fmt.Errorf("use the host probe operation")}
	}
	v, err := s.get(name)
	if err != nil {
		return auditSpec{}, hosts.Host{}, err
	}
	h := v.Host
	if h.Reach.EffectiveTransport() != "outpost" || h.Reach.Outpost == nil {
		return auditSpec{}, h, redact.Guidance("host %q needs reach.transport=outpost for unit operations", name)
	}
	op := hostActionOperation(args.Request.Verb)
	config := map[string]any{"name": name, "unit": args.Request.Unit, "lines": args.Request.Lines}
	// Only the operator's explicit host-to-SSH-resource binding may supply
	// labels. Neither a host name nor a remote unit names a local resource.
	var labels *target.ResourceLabels
	if h.Reach.SSH != "" && s.resources != nil {
		if def, ok := s.resources(h.Reach.SSH); ok && def != nil && def.Type == "server" && def.Connector == "ssh" {
			bound := def.TargetLabels()
			labels = &bound
		}
	}
	resolved := target.Resolve("host", op.Target.Kind, h.Name, labels, false)
	spec := auditSpec{connector: "host", operation: args.Request.Verb, op: op, known: true, config: config,
		acknowledged: args.Acknowledged, approvalID: args.ApprovalID, planOnly: planning, dryRun: planning, resolvedTarget: &resolved}
	spec.plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := auditTarget(spec)
		p := plan.Plan{Lane: plan.LaneAdmin, Connector: "host", Operation: args.Request.Verb, Effect: string(op.Effect), Target: tgt,
			ArgsDigest: s.audit.Digest(config), Digests: map[string]string{"host": s.audit.Digest(h)}}
		if err := p.WithPreview(string(contract.PreviewHost), map[string]any{"host": name, "transport": "outpost", "request": args.Request}); err != nil {
			return p, err
		}
		return p, nil
	}
	return spec, h, nil
}

func (s *HostService) PlanAction(ctx context.Context, name string, args HostActionArgs) (_ *ConnectorPlan, retErr error) {
	spec, _, err := s.actionSpec(name, args, true)
	if err != nil {
		return nil, err
	}
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()
	return showPlan(ctx, spec)
}

func (s *HostService) ExecuteAction(ctx context.Context, name string, args HostActionArgs) (_ outpost.Result, retErr error) {
	spec, h, err := s.actionSpec(name, args, false)
	if err != nil {
		return outpost.Result{}, err
	}
	ctx, scope := redact.EnsureScope(ctx)
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return outpost.Result{}, err
	}
	defer func() { call.finish(retErr) }()
	if err = requireAcknowledgment(ExternalConnectorOperationArgs{Connector: "host", Operation: args.Request.Verb, Acknowledged: args.Acknowledged}, spec.op); err != nil {
		return outpost.Result{}, err
	}
	client := outpost.Client{URL: h.Reach.Outpost.URL, TokenFile: h.Reach.Outpost.TokenFile, Credential: func(token string) { scope.Add("outpost/token", token) }}
	result, err := client.Execute(ctx, args.Request)
	if err != nil {
		return outpost.Result{}, scopeError(scope, err)
	}
	result.Output = scope.Text(result.Output)
	shaped, err := call.applyEgress(result)
	if err != nil {
		return outpost.Result{}, err
	}
	b, err := json.Marshal(shaped)
	if err != nil {
		return outpost.Result{}, err
	}
	var safe outpost.Result
	if err := json.Unmarshal(b, &safe); err != nil {
		return outpost.Result{}, err
	}
	if result.ExitCode != 0 {
		return safe, externalConnectorError(ExternalConnectorOperationArgs{Connector: "host", Operation: args.Request.Verb}, ExternalConnectorOperationFailed, redact.Guidance("host unit operation exited with status %d", result.ExitCode))
	}
	return safe, nil
}

func HostActionDefinition() contract.Definition {
	ops := []contract.Operation{}
	for _, verb := range []string{"units", "logs", "start", "stop", "restart"} {
		ops = append(ops, hostActionOperation(verb))
	}
	ops = append(ops, contract.Operation{Name: "plan", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "host.unit"},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone})
	return contract.Finalize(contract.Definition{ID: "host", Version: "builtin", Operations: ops})
}

func (c *InProcessClient) PlanHostAction(ctx context.Context, name string, args HostActionArgs) (*ConnectorPlan, error) {
	s, err := c.hostService()
	if err != nil {
		return nil, err
	}
	return s.PlanAction(ctx, name, args)
}
func (c *InProcessClient) ExecuteHostAction(ctx context.Context, name string, args HostActionArgs) (outpost.Result, error) {
	s, err := c.hostService()
	if err != nil {
		return outpost.Result{}, err
	}
	return s.ExecuteAction(ctx, name, args)
}

package admin

import (
	"context"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// planOperation is the admin lane's plan (the old one): the operation, its resolved target and
// labels, the keyed digest of its arguments, its dry-run preview where it has one, and for a
// plugin which binary and config would run. It is the one plan an approval is asked for, used
// and shown with (I6).
func (s *Service) planOperation(ctx context.Context, args OperationArgs) (plan.Plan, error) {
	op, err := s.declaredOperation(ctx, args)
	if err != nil {
		return plan.Plan{}, err
	}
	spec := s.specIn(ctx, args, nil)
	tgt, _ := gate.AuditTarget(spec)
	digest := s.gate.Sink().Digest
	p := plan.Plan{Lane: plan.LaneAdmin, Connector: args.Connector, Operation: args.Operation, Effect: string(op.Effect),
		Target: tgt, ArgsDigest: digest(args.Config)}
	if s.plugins.Loaded(args.Connector) {
		if perr := s.plugins.planPlugin(ctx, &p, op, args.Connector, args.Config, args.Acknowledged); perr != nil {
			return plan.Plan{}, perr
		}
		return p, nil
	}
	resolved := args
	switch args.Connector {
	case "ssh":
		if resolved, err = s.resolveSSHTarget(ctx, args); err != nil {
			return plan.Plan{}, err
		}
	case "docker":
		if resolved, err = s.resolveDockerResource(ctx, args); err != nil {
			return plan.Plan{}, err
		}
	}
	if args.Connector == "ssh" || args.Connector == "docker" {
		// The target as it would run, keyed like the arguments (M10): an approval does not
		// outlive an edit to the host, key, context or compose file it was for.
		p.Digests = map[string]string{"target": digest(resolved.Config)}
	}
	if op.Preview != contract.PreviewNone {
		preview, ok, perr := s.dryRunPreview(resolved)
		if perr != nil {
			return plan.Plan{}, perr
		}
		if ok {
			if err := p.WithPreview(string(op.Preview), preview); err != nil {
				return plan.Plan{}, err
			}
		}
	}
	return p, nil
}

// planPlugin adds a plugin's part of a plan: its fingerprints, and its own dry-run preview
// where the operation declares one (a real call to the plugin).
func (p *Plugins) planPlugin(ctx context.Context, pl *plan.Plan, op contract.Operation, id string, config map[string]any, acknowledged bool) error {
	pl.PluginConfigSHA256, pl.PluginEntrypointSHA256 = p.fingerprints(id)
	if op.Preview == contract.PreviewNone {
		return nil
	}
	res, err := p.execute(ctx, id, PluginExecArgs{Operation: op.Name, Config: config, DryRun: true, Acknowledged: acknowledged})
	if err != nil {
		return fmt.Errorf("the plugin's preview, which the plan needs, failed: %w", err)
	}
	return pl.WithPreview(string(op.Preview), res.Data)
}

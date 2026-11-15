package gate

import (
	"context"
	"errors"

	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The plan functions: one per lane, used when an approval is asked for and
// again when it is used, so the two cannot be computed differently (I6). A
// lane supplies its own as Spec.Plan; the gate never computes a lane's plan
// itself (the old build's admin, resource and pipeline planners lived beside
// their services, which are not the gate's).

// ConnectorPlan is a plan shown on request: what an approval of the call
// would bind to, and the hash it would record.
type ConnectorPlan struct {
	PlanHash string    `json:"plan_hash"`
	Plan     plan.Plan `json:"plan"`
	// ComputedBy is the surface of the process that computed the plan:
	// socket (the daemon), in_process (the CLI's own) or web. It is not part
	// of the plan: the same call can plan differently in different
	// processes, since a pipeline's shell steps list the environment of the
	// process that runs them (CERB-GAP-882).
	ComputedBy CallerSurface `json:"computed_by"`
}

// ShowPlan answers a plan request (Spec.PlanOnly) with the lane's own plan
// function — the one an approval is asked for and used with — so what is
// shown is what would be bound. It runs nothing but the preview. The lane
// begins the call first, so the request is gated and recorded as the dry
// run it is, and finishes it with ShowPlan's error.
func (c *Call) ShowPlan(ctx context.Context) (*ConnectorPlan, error) {
	return showPlan(ctx, c.Spec())
}

// showPlan computes a spec's plan through its lane's plan function and
// hashes it, for a caller that asked to see it.
func showPlan(ctx context.Context, spec Spec) (*ConnectorPlan, error) {
	args := opArgs{Connector: spec.Connector, Operation: spec.Operation}
	if spec.Plan == nil {
		return nil, codedError(args, CodeUnsupported, redact.Guidance("%s %s has no plan to show", spec.Connector, spec.Operation))
	}
	p, err := spec.Plan(ctx)
	if err != nil {
		var coded *Error
		if errors.As(err, &coded) {
			return nil, err
		}
		return nil, codedError(args, CodeInvalidArgs, err)
	}
	p.V = plan.Version
	hash, err := p.Hash()
	if err != nil {
		return nil, codedError(args, CodeInvalidArgs, err)
	}
	return &ConnectorPlan{PlanHash: hash, Plan: p, ComputedBy: CallerSurfaceFrom(ctx)}, nil
}

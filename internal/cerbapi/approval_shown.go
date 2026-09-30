package cerbapi

import (
	"context"
	"encoding/json"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// planSnapshot is what an approval request binds to and shows: the plan's
// hash, and the approver's view of the plan and the arguments.
type planSnapshot struct {
	hash  string
	shown *approval.Shown
}

// specPlanSnapshot computes the lane's plan for the call once, hashes it, and
// renders what the approver is shown. A lane without a plan function binds
// to the arguments alone, and still shows them.
func specPlanSnapshot(ctx context.Context, spec auditSpec) (planSnapshot, error) {
	var p *plan.Plan
	var snap planSnapshot
	if spec.plan != nil {
		computed, err := spec.plan(ctx)
		if err != nil {
			return planSnapshot{}, err
		}
		if snap.hash, err = computed.Hash(); err != nil {
			return planSnapshot{}, err
		}
		p = &computed
	}
	snap.shown = renderShown(redact.ScopeFrom(ctx), p, spec.config)
	return snap, nil
}

// renderShown renders the approver's view: the plan and the arguments as
// JSON through the request's redaction scope, so a credential value the
// request resolved never lands in the approvals store, and the text rules
// run over the rest. The arguments, and a preview that echoes them, are
// marked untrusted: an agent wrote them.
func renderShown(scope *redact.Scope, p *plan.Plan, args map[string]any) *approval.Shown {
	shown := &approval.Shown{}
	if p != nil {
		if data, err := scope.Marshal(p); err == nil {
			shown.Plan = data
		}
		if len(p.Preview) > 0 {
			shown.Untrusted = append(shown.Untrusted, "/plan/preview")
		}
	}
	if len(args) > 0 {
		if data, err := scope.Marshal(args); err == nil {
			shown.Arguments = data
			shown.Untrusted = append(shown.Untrusted, "/arguments")
		}
	}
	if len(shown.Plan) == 0 && len(shown.Arguments) == 0 {
		return nil
	}
	if len(shown.Plan)+len(shown.Arguments) > approval.ShownMaxBytes {
		// Keep the plan whole where it fits, and say what was cut. A cut
		// JSON document is not JSON, so what does not fit is replaced by a
		// note, never truncated mid-value.
		shown.Truncated = true
		note, _ := json.Marshal("[cerberus: too large to store with the approval; the approval still binds the whole plan by its hash. Deny it and ask for a smaller call if you cannot see what it does]")
		if len(shown.Plan) > approval.ShownMaxBytes {
			shown.Plan = note
		}
		if len(shown.Plan)+len(shown.Arguments) > approval.ShownMaxBytes {
			shown.Arguments = note
		}
	}
	return shown
}

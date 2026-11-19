package gate

import (
	"context"
	"errors"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// The data lane's hooks into the gate (slice 7d: `cerberus data query`, docs/data-access.md):
// the old build's BindDataService admitted a session-bound data query with the brakes and an
// explicit policy allow before it ran and again before it delivered, and wrote its own
// fail-closed audit pair beside the connector gate's records. These are those two checks, on
// this gate's brakes, decision point and sink.

// ErrDataDenied is a data query the brakes or policy do not allow.
var ErrDataDenied = errors.New("data access refused by the brakes or policy")

// DataAuthorize admits a data query described by spec for ctx's principal: no brake refuses
// it, and the decision point explicitly allows it (an approve is not an allow: a data grant
// never widens policy, and there is no one to approve mid-query).
func (g *Gate) DataAuthorize(ctx context.Context, spec Spec) error {
	_, resolved := auditTarget(spec)
	if g.brakeRefusal(ctx, spec, resolved, false) != nil {
		return ErrDataDenied
	}
	if g.pdp == nil || g.pdp.Authorize(policyRequest(ctx, spec, resolved)).Decision != policy.Allow {
		return ErrDataDenied
	}
	return nil
}

// DataRecord writes rec, one of a data query's audit pair, with ctx's principal, spec's
// target, and the shadow decision and posture it was evaluated under. An error is a record not
// written: the caller fails closed.
func (g *Gate) DataRecord(ctx context.Context, spec Spec, rec audit.Record) error {
	t, resolved := auditTarget(spec)
	rec.Principal, rec.Target = principalFor(ctx, spec), t
	rec.Policy, rec.Posture = g.shadowDecision(ctx, spec, resolved)
	_, err := g.sink.Write(rec)
	return err
}

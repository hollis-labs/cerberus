package supervise

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Request is one mutation as a caller asked for it: the verb, the resource, and the old
// build's mutation options (docs/substrate-port/legacy-routes.md, L7 and L8).
type Request struct {
	Verb string
	ID   string
	// Acknowledged is the caller's --ack. Every verb needs it, as in the old build.
	Acknowledged bool
	// ApprovalID names an approval to run under.
	ApprovalID string
	// Plan asks for the plan instead of running.
	Plan bool
	// ConfirmedPlanHash runs a plan the caller confirmed.
	ConfirmedPlanHash string
	// InstallAfterBuildOverride is the CLI's --install-after-build; nil means no override.
	InstallAfterBuildOverride *bool
}

// Refusal is a mutation refused before it ran. It is not an OpResult: the API answers with
// Status and the flat {code, message} body, as the old build did for its gate.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// Approver decides a mutation before it runs. res is nil for a resource the registry does not
// list; the run then reports it as not found. Slice 6 implements the policy gate and
// approvals behind this interface.
type Approver interface {
	Approve(ctx context.Context, req Request, res *config.ResourceDef) error
}

// requireAck is the old build's runtime gate (cerbapi resourceGate): every local operation
// needs an acknowledgment, checked before the resource is looked up.
func requireAck(req Request) error {
	op, ok := operation(req.Verb)
	if !ok {
		return &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("unknown resource action %q", req.Verb)}
	}
	if op.RequiresAck && !req.Acknowledged {
		return &Refusal{Status: http.StatusConflict, Code: "acknowledgment_required", Message: fmt.Sprintf(
			"%s of %q changes the workload, so it needs an acknowledgment: run it again with --ack (\"acknowledged\": true)", req.Verb, req.ID)}
	}
	return nil
}

// operation is the local connector's definition of a verb.
func operation(verb string) (contract.Operation, bool) {
	for _, op := range localconn.Definition().Operations {
		if op.Name == verb {
			return op, true
		}
	}
	return contract.Operation{}, false
}

// InterimApprover is the approver of the build before slice 6, and only of that build: there
// is no policy gate and no approvals store yet. It lets through an effect that needs only an
// acknowledgment, which is what the old build does under its baseline posture. It refuses
// everything the old build routes to an approval or a typed confirmation: a request naming an
// approval, a plan or a confirmed plan, and a target a lockdown or a freeze covers. cmd refuses
// to start in supervise mode with this approver and a non-empty policy.
type InterimApprover struct {
	// Brakes returns the brake state; nil means none engaged.
	Brakes func() brake.State
}

// ErrApprovalsUnavailable is the refusal code of every request the interim approver refuses.
const ErrApprovalsUnavailable = "approvals_unavailable"

// Approve implements Approver.
func (a InterimApprover) Approve(_ context.Context, req Request, res *config.ResourceDef) error {
	refuse := func(status int, why string) error {
		return &Refusal{Status: status, Code: ErrApprovalsUnavailable, Message: fmt.Sprintf(
			"%s of %q %s, and this Cerberus has no approvals yet (they arrive with slice 6 of the port); run it with the old Cerberus, or without that option", req.Verb, req.ID, why)}
	}
	switch {
	case req.ApprovalID != "":
		return refuse(http.StatusPreconditionRequired, "names an approval")
	case req.Plan:
		return refuse(http.StatusPreconditionRequired, "asks for a plan")
	case req.ConfirmedPlanHash != "":
		return refuse(http.StatusPreconditionRequired, "confirms a plan")
	}
	if a.Brakes == nil {
		return nil
	}
	op, ok := operation(req.Verb)
	if !ok {
		return nil
	}
	var labels *target.ResourceLabels
	if res != nil {
		l := res.TargetLabels()
		labels = &l
	}
	t := target.Resolve("local", op.Target.Kind, req.ID, labels, false)
	if blocked, lockdown, freeze := a.Brakes().Blocks("local", op.Effect, t); blocked {
		switch {
		case lockdown != nil:
			return refuse(http.StatusLocked, "is refused by a lockdown, which only an approval overrides")
		case freeze != nil:
			return refuse(http.StatusLocked, fmt.Sprintf("is refused by freeze %s, which only an approval overrides", freeze.ID))
		}
	}
	return nil
}

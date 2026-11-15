package supervise

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
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

// Refusal is a mutation the lane refused before the gate: observe mode, a missing
// acknowledgment or an unknown verb. It is not an OpResult: the API answers with Status and
// the flat {code, message} body. The gate's own refusals (policy, approvals, brakes) are
// *gate.Error, which the API maps with gate.HTTPError.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// requireAck is the old build's runtime gate (cerbapi resourceGate): every local operation
// needs an acknowledgment, checked before the resource is looked up and before the operation
// gate, so a request without one leaves no record.
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
	return localconn.Definition().Operation(verb)
}

// mutationSpec is the gate's spec for req, as the old build's recordMutation built it: the
// local connector's operation, the id and the deploy override as its arguments, the target
// labeled from cfg, and the lane's plan function. A plan request spends no approval and no
// confirmation.
func (s *Service) mutationSpec(cfg *config.ConfigV2, req Request) gate.Spec {
	op, known := operation(req.Verb)
	args := map[string]any{localconn.InputID: req.ID}
	if req.InstallAfterBuildOverride != nil {
		args[localconn.InputInstallAfterBuildOverride] = *req.InstallAfterBuildOverride
	}
	spec := gate.Spec{
		Connector: localconn.Definition().ID, Operation: req.Verb, Op: op, Known: known,
		Config: args, Acknowledged: req.Acknowledged, Resources: gate.ConfigResourceLookup(cfg),
		ApprovalID: req.ApprovalID, PlanOnly: req.Plan, DryRun: req.Plan, ConfirmedPlanHash: req.ConfirmedPlanHash,
	}
	if req.Plan {
		spec.ApprovalID, spec.ConfirmedPlanHash = "", ""
	}
	planSpec := spec
	spec.Plan = func(ctx context.Context) (plan.Plan, error) { return s.planResource(ctx, cfg, planSpec, req.ID) }
	return spec
}

// errOperationReportedFailure is the outcome of a call that returned no error but a result
// reporting failure: an OpResult with success false.
var errOperationReportedFailure = errors.New("the operation reported failure")

// resultError is what an outcome is coded from: the call's error, or, when it returned none
// but its result says it failed, operation_failed.
func resultError(verb string, err error, failed bool) error {
	if err != nil || !failed {
		return err
	}
	return gate.NewError(localconn.Definition().ID, verb, gate.CodeOperationFailed, errOperationReportedFailure)
}

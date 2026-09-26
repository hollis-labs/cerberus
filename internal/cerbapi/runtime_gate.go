package cerbapi

import (
	"context"
	"log/slog"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/plan"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// RuntimeDefinitions are the contracts of the supervision lane's mutations,
// pipeline runs and deployment-profile runs. They are not admin-lane connectors — nothing
// dispatches them through ExternalConnectorService — but they are gated on
// the same contract, and conformance enumerates them with the rest.
func RuntimeDefinitions() []contract.Definition {
	return []contract.Definition{localconn.Definition(), pipeline.Definition(), infra.Definition(), ControlPlaneDefinition()}
}

// RunDeploymentProfile runs a saved deployment profile through the contract
// gate. It is exec — the profile's commands run in a shell — so it needs the
// caller's acknowledgment, which the console gives from a confirm step that
// shows the plan (infra.PlanDeployment) it is about to run.
//
// It is recorded like every operation: an intent before the gate and an
// outcome after, under the caller's audit sink.
func RunDeploymentProfile(ctx context.Context, sink audit.Sink, secrets secret.Provider, profile infra.DeploymentProfile, options ...MutationOption) (*infra.DeploymentRunResult, error) {
	opts := ApplyMutationOptions(options)
	def := infra.Definition()
	spec := deployProfileSpec(profile, opts)
	planSpec := spec
	// checked is the deployment plan the gate hashed, when it hashed one:
	// the run executes it rather than planning again (CERB-GAP-878).
	var checked *infra.DeploymentPlan
	spec.plan = func(ctx context.Context) (plan.Plan, error) {
		p, dp, err := planDeploymentProfile(ctx, planSpec, sink, secrets, profile)
		checked = dp
		return p, err
	}
	call, err := beginGated(ctx, sink, slog.Default(), spec)
	if err != nil {
		return nil, err
	}
	if gateErr := runtimeGate(ctx, def, infra.OpRunProfile, map[string]any{"id": profile.ID}, opts); gateErr != nil {
		call.finish(gateErr)
		return nil, gateErr
	}
	var result *infra.DeploymentRunResult
	if checked != nil {
		result, err = infra.RunPlannedDeployment(ctx, checked, profile)
	} else {
		result, err = infra.RunDeployment(ctx, secrets, profile)
	}
	call.finish(resultError(err, result != nil && !result.Success))
	return result, err
}

// deployProfileSpec is a deploy-profile run's audit spec.
func deployProfileSpec(profile infra.DeploymentProfile, opts MutationOpts) auditSpec {
	def := infra.Definition()
	op, known := def.Operation(infra.OpRunProfile)
	return auditSpec{
		connector: def.ID, operation: infra.OpRunProfile, op: op, known: known,
		config: map[string]any{"id": profile.ID}, acknowledged: opts.Acknowledged,
		// The run reads the Vercel token and scope to pass on the command line.
		credentials:       []string{"vercel/scope", "vercel/token"},
		approvalID:        opts.ApprovalID,
		confirmedPlanHash: opts.ConfirmedPlanHash,
	}
}

// PlanDeploymentProfile is a deploy-profile run's plan and its hash, as an
// approval of the run would bind it: the console's confirm step shows it
// and sends the hash back (P3-3b). It runs nothing and is recorded as a dry
// run; it resolves the Vercel token to plan, as a run does.
func PlanDeploymentProfile(ctx context.Context, sink audit.Sink, secrets secret.Provider, profile infra.DeploymentProfile, options ...MutationOption) (*ConnectorPlan, error) {
	opts := ApplyMutationOptions(options)
	spec := deployProfileSpec(profile, opts)
	spec.planOnly, spec.dryRun, spec.approvalID, spec.confirmedPlanHash = true, true, "", ""
	planSpec := spec
	spec.plan = func(ctx context.Context) (plan.Plan, error) {
		p, _, err := planDeploymentProfile(ctx, planSpec, sink, secrets, profile)
		return p, err
	}
	call, err := beginGated(ctx, sink, slog.Default(), spec)
	if err != nil {
		return nil, err
	}
	shown, err := showPlan(ctx, spec)
	call.finish(err)
	return shown, err
}

// runtimeGate is the contract gate for a resource mutation or a pipeline run:
// the operation must be declared, its config must pass the key table for the
// caller's surface, and it needs the caller's acknowledgment. It runs before
// the operation takes a lock or looks anything up, so a refusal depends on
// nothing but the request.
//
// Supervision is not an operation request: the resource monitor restarts a
// crashed workload through the local connector directly and never reaches
// this gate (Decision 14).
func runtimeGate(ctx context.Context, def contract.Definition, operation string, config map[string]any, opts MutationOpts) error {
	args := ExternalConnectorOperationArgs{Connector: def.ID, Operation: operation, Config: config, Acknowledged: opts.Acknowledged}
	op, ok := def.Operation(operation)
	if !ok {
		return externalConnectorError(args, ExternalConnectorUnsupported, redact.Guidance("%s does not declare operation %q; refusing", def.ID, operation))
	}
	if err := op.CheckInputs(config, CallerSurfaceFrom(ctx) == SurfaceInProcess); err != nil {
		return externalConnectorError(args, ExternalConnectorInvalidArgs, redact.Prose(err))
	}
	if op.RequiresAck && !opts.Acknowledged {
		return externalConnectorError(args, ExternalConnectorAckRequired, redact.Guidance("%s operation %q on %q requires operator acknowledgment", op.Effect, operation, config["id"]))
	}
	return nil
}

// resourceGate gates a resource mutation on the local connector's contract.
func resourceGate(ctx context.Context, operation, id string, opts MutationOpts) error {
	config := map[string]any{localconn.InputID: id}
	if opts.InstallAfterBuildOverride != nil {
		config[localconn.InputInstallAfterBuildOverride] = *opts.InstallAfterBuildOverride
	}
	return runtimeGate(ctx, localconn.Definition(), operation, config, opts)
}

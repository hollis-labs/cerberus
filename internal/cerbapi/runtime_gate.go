package cerbapi

import (
	"context"
	"fmt"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/pipeline"
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
func RunDeploymentProfile(ctx context.Context, secrets secret.Provider, profile infra.DeploymentProfile, options ...MutationOption) (*infra.DeploymentRunResult, error) {
	if err := runtimeGate(ctx, infra.Definition(), infra.OpRunProfile, map[string]any{"id": profile.ID}, ApplyMutationOptions(options)); err != nil {
		return nil, err
	}
	return infra.RunDeployment(ctx, secrets, profile)
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
		return externalConnectorError(args, ExternalConnectorUnsupported, fmt.Errorf("%s does not declare operation %q; refusing", def.ID, operation))
	}
	if err := op.CheckInputs(config, CallerSurfaceFrom(ctx) == SurfaceInProcess); err != nil {
		return externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	if op.RequiresAck && !opts.Acknowledged {
		return externalConnectorError(args, ExternalConnectorAckRequired, fmt.Errorf("%s operation %q on %q requires operator acknowledgment", op.Effect, operation, config["id"]))
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

package cerbapi

import (
	"context"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// RuntimeDefinitions are the contracts of the supervision lane's mutations
// and pipeline runs. They are not admin-lane connectors — nothing
// dispatches them through ExternalConnectorService — but they are gated on
// the same contract, and conformance enumerates them with the rest.
func RuntimeDefinitions() []contract.Definition {
	return []contract.Definition{localconn.Definition(), pipeline.Definition(), ControlPlaneDefinition()}
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

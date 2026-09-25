package cerbapi

import (
	"context"
	"errors"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/pipeline"
)

// The supervision lane's operations are recorded like the admin lane's: an
// intent before the contract gate, so a refusal is recorded too, and an
// outcome on every exit. Each public method below wraps its implementation.

// DeployResource builds, installs and applies a resource. Recorded.
func (s *ResourceRuntimeService) DeployResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpDeploy, id, options, s.deployResource)
}

// ApplyResource applies a resource through its runtime backend. Recorded.
func (s *ResourceRuntimeService) ApplyResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpApply, id, options, s.applyResource)
}

// ReloadResource restarts a resource without rebuilding it. Recorded.
func (s *ResourceRuntimeService) ReloadResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpReload, id, options, s.reloadResource)
}

// StopResource stops a resource, keeping its install state. Recorded.
func (s *ResourceRuntimeService) StopResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpStop, id, options, s.stopResource)
}

// SyncResource copies the built artifact into place. Recorded.
func (s *ResourceRuntimeService) SyncResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpSync, id, options, s.syncResource)
}

// RemoveResource uninstalls a resource's runtime state. Recorded.
func (s *ResourceRuntimeService) RemoveResource(ctx context.Context, id string, options ...MutationOption) (*OpResult, error) {
	return s.recordMutation(ctx, localconn.OpRemove, id, options, s.removeResource)
}

// RunPipeline runs a pipeline's stages. Recorded as one operation: the stages
// call the local connector directly and are covered by the run's record.
func (s *ResourceRuntimeService) RunPipeline(ctx context.Context, id string, options ...MutationOption) (*PipelineRunResult, error) {
	opts := ApplyMutationOptions(options)
	def := pipeline.Definition()
	op, known := def.Operation(pipeline.OpRun)
	call, err := beginAudit(ctx, s.audit, s.logger, auditSpec{
		connector: def.ID, operation: pipeline.OpRun, op: op, known: known,
		config: map[string]any{"id": id}, acknowledged: opts.Acknowledged,
	})
	if err != nil {
		return nil, externalConnectorError(ExternalConnectorOperationArgs{Connector: def.ID, Operation: pipeline.OpRun}, ExternalConnectorAuditUnavailable, err)
	}
	out, err := s.runPipeline(ctx, id, options...)
	call.finish(resultError(err, out != nil && !out.Success))
	return out, err
}

func (s *ResourceRuntimeService) recordMutation(ctx context.Context, operation, id string, options []MutationOption,
	run func(context.Context, string, ...MutationOption) (*OpResult, error)) (*OpResult, error) {
	opts := ApplyMutationOptions(options)
	config := map[string]any{localconn.InputID: id}
	if opts.InstallAfterBuildOverride != nil {
		config[localconn.InputInstallAfterBuildOverride] = *opts.InstallAfterBuildOverride
	}
	def := localconn.Definition()
	op, known := def.Operation(operation)
	call, err := beginAudit(ctx, s.audit, s.logger, auditSpec{
		connector: def.ID, operation: operation, op: op, known: known,
		config: config, acknowledged: opts.Acknowledged, resources: s.ResourceDef,
	})
	if err != nil {
		return nil, externalConnectorError(ExternalConnectorOperationArgs{Connector: def.ID, Operation: operation}, ExternalConnectorAuditUnavailable, err)
	}
	out, err := run(ctx, id, options...)
	call.finish(resultError(err, out != nil && !out.Success))
	return out, err
}

// errOperationReportedFailure is the outcome of a call that returned no error
// but a result reporting failure — an OpResult with success false.
var errOperationReportedFailure = errors.New("the operation reported failure")

// resultError is what an outcome is coded from: the call's error, or, when
// it returned none but its result says it failed, operation_failed.
func resultError(err error, failed bool) error {
	if err != nil || !failed {
		return err
	}
	return &ExternalConnectorError{Code: ExternalConnectorOperationFailed, Err: errOperationReportedFailure}
}

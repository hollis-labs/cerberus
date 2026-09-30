package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/target"

	"github.com/hollis-labs/cerberus/internal/plan"

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
	// The run's target carries the labels of what its stages change, so an
	// agent's run of a pipeline that restarts a production resource is an
	// agent's change to production (B2): its stages call the connector
	// directly and pass no gate of their own. One read of the definition
	// labels it, and is the one planned and run.
	read, problem := s.lookupPipeline(id)
	spec := auditSpec{
		connector: def.ID, operation: pipeline.OpRun, op: op, known: known,
		config: map[string]any{"id": id}, acknowledged: opts.Acknowledged, resources: pipelineTargetLookup(id, read),
		approvalID: opts.ApprovalID, planOnly: opts.Plan, dryRun: opts.Plan, confirmedPlanHash: opts.ConfirmedPlanHash, breakGlass: opts.BreakGlass,
	}
	if opts.Plan {
		spec.approvalID, spec.confirmedPlanHash, spec.breakGlass = "", "", nil
	}
	planSpec := spec
	// checked is the definition the gate hashed, when it hashed one: the run
	// executes it rather than reading the config again.
	checked := read
	spec.plan = func(ctx context.Context) (plan.Plan, error) {
		p, snap, err := s.planPipelineSnapshot(ctx, planSpec, id, read, problem)
		checked = snap
		return p, err
	}
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return nil, err
	}
	if opts.Plan {
		shown, planErr := showPlan(ctx, spec)
		call.finish(planErr)
		if planErr != nil {
			return nil, planErr
		}
		return &PipelineRunResult{Success: true, Plan: shown}, nil
	}
	// Where the gate hashed no plan, the definition is read once here, and
	// the freeze check and the run both use that read (M10).
	if checked == nil {
		checked, _ = s.lookupPipeline(id)
	}
	// A pipeline does not run while any resource it touches is frozen (§12);
	// a lockdown has already refused it at the gate.
	if refusal := s.pipelineFrozen(checked); refusal != nil {
		call.finish(refusal)
		return nil, refusal
	}
	out, err := s.runPipeline(ctx, id, checked, options...)
	if err == nil && out != nil && len(out.Raw) > 0 {
		out, err = shapePipelineResult(call, out)
	}
	call.finish(resultError(err, out != nil && !out.Success))
	return out, err
}

// shapePipelineResult is egress policy on a pipeline run (P4-4): its body
// travels as the executor's JSON, so it is shaped as pipeline.RunResult and
// written back.
func shapePipelineResult(call *auditCall, out *PipelineRunResult) (*PipelineRunResult, error) {
	run, err := out.Execution()
	if err != nil {
		return out, nil //nolint:nilerr // a body egress cannot read passes as it came; the run itself succeeded
	}
	shaped, err := shapeAs(call, run)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(shaped)
	if err != nil {
		return nil, err
	}
	next := *out
	next.Raw = raw
	return &next, nil
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
	// One resolve for the whole call (M10): the gate labels the target, the
	// plan hashes the definition and the run executes it, all from this.
	snap := s.snapshotConfig()
	ctx = withConfigSnapshot(ctx, snap)
	// A deploy's checkout is read once too: the plan hashes it, and the
	// build refuses a tree that changed since (M10).
	if operation == localconn.OpDeploy {
		if res := findResourceDef(snap, id); res != nil {
			if pspec, perr := localconn.SpecFromResourceConfig(res.Config); perr == nil && localconn.HasBuildStrategy(pspec) {
				ctx = withCheckedSource(ctx, pspec.Dir)
			}
		}
	}
	spec := auditSpec{
		connector: def.ID, operation: operation, op: op, known: known,
		config: config, acknowledged: opts.Acknowledged, resources: resourcesIn(snap),
		approvalID: opts.ApprovalID, planOnly: opts.Plan, dryRun: opts.Plan, confirmedPlanHash: opts.ConfirmedPlanHash, breakGlass: opts.BreakGlass,
	}
	if opts.Plan {
		spec.approvalID, spec.confirmedPlanHash, spec.breakGlass = "", "", nil
	}
	planSpec := spec
	spec.plan = func(ctx context.Context) (plan.Plan, error) { return s.planResource(ctx, planSpec, id) }
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return nil, err
	}
	if opts.Plan {
		shown, planErr := showPlan(ctx, spec)
		call.finish(planErr)
		if planErr != nil {
			return nil, planErr
		}
		return &OpResult{Success: true, ServiceID: id, Message: "plan only; nothing ran", Plan: shown}, nil
	}
	out, err := run(ctx, id, options...)
	if err == nil && out != nil && out.Success && appliedVerbs[operation] {
		if res := findResourceDef(snap, id); res != nil {
			if aerr := s.applied.set(id, s.definitionDigest(res)); aerr != nil {
				s.logger.Warn("resource_runtime.applied_record_failed", "resource", id, "error", aerr.Error())
			}
		}
	}
	if err == nil && out != nil {
		// Egress policy on what comes back (P4-4): build and install
		// output are untrusted.
		out, err = shapeAs(call, out)
	}
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

// pipelineChanges are the actions that change the resource they name.
var pipelineChanges = map[string]bool{"deploy": true, "deploy_app": true, "start": true, "stop": true}

// pipelineTargetLookup labels a pipeline run's target with the most
// protective labels among the resources its stages change: production if
// any is, owner-administered (or shared) if any is, and otherwise the
// operator's own dev pipeline. A read that found no pipeline
// labels nothing, and the run then fails on the missing definition.
func pipelineTargetLookup(id string, snap *pipelineSnapshot) func(string) (*config.ResourceDef, bool) {
	return func(name string) (*config.ResourceDef, bool) {
		if name != id || snap == nil {
			return nil, false
		}
		// The pipeline is the operator's own; it takes a stricter label only
		// from what its stages change.
		def := config.ResourceDef{ID: id, Type: "pipeline", Connector: "pipeline", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}
		byID := map[string]config.ResourceDef{}
		for _, r := range snap.resources {
			byID[r.ID] = r
		}
		for _, stage := range snap.def.Stages {
			for _, action := range stage.Actions {
				r, ok := byID[action.Resource]
				if !ok || !pipelineChanges[action.Type] {
					continue
				}
				if r.Env == target.EnvProd {
					def.Env = target.EnvProd
				}
				switch {
				case r.Admin.Default == target.AdminOwner:
					def.Admin.Default = target.AdminOwner
				case r.Admin.Default == target.AdminShared && def.Admin.Default != target.AdminOwner:
					def.Admin.Default = target.AdminShared
				}
			}
		}
		return &def, true
	}
}

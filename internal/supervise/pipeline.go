package supervise

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/target"
)

// Pipelines, as the old build ran them (internal/cerbapi pipeline_service.go, runtime_audit.go
// RunPipeline, plans.go planPipeline, brakes.go pipelineFrozen). A run passes the gate once as
// pipeline.run: its stages call the local connector directly and are covered by that record.
// Scheduled runs are not served here (the scheduler is off in this build).

// PipelineInfo is a configured pipeline, as the old list answered.
type PipelineInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Stages      int    `json:"stage_count"`
}

// PipelineDetail is the inspectable definition, including resolution diagnostics.
type PipelineDetail struct {
	Definition      config.PipelineDef `json:"definition"`
	ValidationError string             `json:"validation_error,omitempty"`
}

// PipelineRunResult is the old run answer: the executor's result as JSON in Raw, or the plan.
type PipelineRunResult struct {
	Success bool                `json:"success"`
	Error   string              `json:"error,omitempty"`
	Raw     []byte              `json:"raw,omitempty"`
	Plan    *gate.ConnectorPlan `json:"plan,omitempty"`
}

// PipelineRequest is one run as a caller asked for it.
type PipelineRequest struct {
	ID           string
	Acknowledged bool
	ApprovalID   string
	// Plan asks for the run's plan instead of running.
	Plan bool
	// ConfirmedPlanHash runs a plan the caller confirmed.
	ConfirmedPlanHash string
}

// pipelineSnapshot is the definition a run executes: the pipeline and the resources it
// resolves against, as its plan hashed them.
type pipelineSnapshot struct {
	def       config.PipelineDef
	resources []config.ResourceDef
}

// ListPipelines lists the configured pipelines.
func (s *Service) ListPipelines() ([]PipelineInfo, error) {
	cfg, err := s.config()
	if err != nil {
		return nil, err
	}
	out := make([]PipelineInfo, 0, len(cfg.Pipelines))
	for _, p := range cfg.Pipelines {
		out = append(out, PipelineInfo{ID: p.ID, Name: p.Name, Description: p.Description, Stages: len(p.Stages)})
	}
	return out, nil
}

// GetPipeline returns a definition and any resolution error without executing it; nil when
// no pipeline has the id.
func (s *Service) GetPipeline(id string) (*PipelineDetail, error) {
	cfg, err := s.config()
	if err != nil {
		return nil, err
	}
	for _, def := range cfg.Pipelines {
		if def.ID != id {
			continue
		}
		detail := &PipelineDetail{Definition: def}
		if _, err := pipeline.Resolve(def, cfg.Resources, s.conn); err != nil {
			detail.ValidationError = fmt.Sprintf("resolve pipeline: %s", err)
		}
		return detail, nil
	}
	return nil, nil
}

// lookupPipeline is the pipeline and the resources as the config reads now.
func (s *Service) lookupPipeline(id string) (*pipelineSnapshot, string) {
	cfg, err := s.config()
	if err != nil {
		return nil, "no config available"
	}
	for i := range cfg.Pipelines {
		if cfg.Pipelines[i].ID == id {
			return &pipelineSnapshot{def: clonePipelineDefinition(cfg.Pipelines[i]), resources: append([]config.ResourceDef(nil), cfg.Resources...)}, ""
		}
	}
	return nil, fmt.Sprintf("pipeline %q not found in config", id)
}

// RunPipeline runs a pipeline's stages, recorded as one operation (the old RunPipeline).
func (s *Service) RunPipeline(ctx context.Context, req PipelineRequest) (*PipelineRunResult, error) {
	if s.o.Mode != settings.ModeSupervise {
		return nil, s.observeRefusal("run of pipeline", req.ID)
	}
	def := pipeline.Definition()
	op, known := def.Operation(pipeline.OpRun)
	// The run's target carries the labels of what its stages change, so an agent's run of a
	// pipeline that restarts a production resource is an agent's change to production (B2).
	// One read of the definition labels it, and is the one planned and run (M10).
	read, problem := s.lookupPipeline(req.ID)
	args := map[string]any{"id": req.ID}
	spec := gate.Spec{Connector: def.ID, Operation: pipeline.OpRun, Op: op, Known: known, Config: args,
		Acknowledged: req.Acknowledged, Resources: pipelineTargetLookup(req.ID, read), ApprovalID: req.ApprovalID,
		PlanOnly: req.Plan, DryRun: req.Plan, ConfirmedPlanHash: req.ConfirmedPlanHash}
	if req.Plan {
		spec.ApprovalID, spec.ConfirmedPlanHash = "", ""
	}
	planSpec := spec
	// checked is the definition the gate hashed, when it hashed one: the run executes it
	// rather than reading the config again.
	checked := read
	spec.Plan = func(ctx context.Context) (plan.Plan, error) {
		p, snap, err := s.planPipelineSnapshot(ctx, planSpec, req.ID, read, problem)
		checked = snap
		return p, err
	}
	call, err := s.o.Gate.Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	if req.Plan {
		shown, planErr := call.ShowPlan(ctx)
		call.Finish(planErr)
		if planErr != nil {
			return nil, planErr
		}
		return &PipelineRunResult{Success: true, Plan: shown}, nil
	}
	if checked == nil {
		checked, _ = s.lookupPipeline(req.ID)
	}
	// A pipeline does not run while any resource it touches is frozen (§12); a lockdown has
	// already refused it at the gate.
	if refusal := s.pipelineFrozen(checked); refusal != nil {
		call.Finish(refusal)
		return nil, refusal
	}
	out, err := s.runPipeline(ctx, req, checked)
	if err == nil && out != nil && len(out.Raw) > 0 {
		out, err = shapePipelineResult(call, out)
	}
	call.Finish(pipelineResultError(err, out != nil && !out.Success))
	return out, err
}

// runPipeline runs the checked snapshot, or the config as it reads now.
func (s *Service) runPipeline(ctx context.Context, req PipelineRequest, snap *pipelineSnapshot) (*PipelineRunResult, error) {
	def := pipeline.Definition()
	op, _ := def.Operation(pipeline.OpRun)
	args := map[string]any{"id": req.ID}
	if err := op.CheckInputs(args, gate.CallerSurfaceFrom(ctx) == gate.SurfaceInProcess); err != nil {
		return nil, gate.NewError(def.ID, pipeline.OpRun, gate.CodeInvalidArgs, redact.Prose(err))
	}
	if op.RequiresAck && !req.Acknowledged {
		return nil, gate.NewError(def.ID, pipeline.OpRun, gate.CodeAckRequired,
			redact.Guidance("%s operation %q on %q requires operator acknowledgment", op.Effect, pipeline.OpRun, req.ID))
	}
	if snap == nil {
		var problem string
		if snap, problem = s.lookupPipeline(req.ID); snap == nil {
			return &PipelineRunResult{Success: false, Error: problem}, nil
		}
	}
	s.pipelineMu.Lock()
	defer s.pipelineMu.Unlock()
	p, err := pipeline.Resolve(snap.def, append([]config.ResourceDef(nil), snap.resources...), s.conn)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("resolve pipeline: %s", err.Error())}, nil
	}
	env := &domain.PipelineEnv{Values: make(map[string]any)}
	// The run is the caller's; its stages are Cerberus acting for them.
	result, err := pipeline.NewExecutor(nil).Run(gate.WithPrincipal(ctx, gate.PipelinePrincipal(ctx, req.ID)), p, env)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline execution: %s", err.Error())}, nil
	}
	raw, err := redact.ScopeFrom(ctx).Marshal(result)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("marshal result: %s", err.Error())}, nil
	}
	return &PipelineRunResult{Success: true, Raw: raw}, nil
}

// pipelineResultError is what a run's outcome is coded from: the error, or operation_failed
// for a result that reports failure.
func pipelineResultError(err error, failed bool) error {
	if err != nil || !failed {
		return err
	}
	return gate.NewError(pipeline.Definition().ID, pipeline.OpRun, gate.CodeOperationFailed, errOperationReportedFailure)
}

// shapePipelineResult is egress policy on a run (P4-4): its body travels as the executor's
// JSON, so it is shaped as pipeline.RunResult and written back.
func shapePipelineResult(call *gate.Call, out *PipelineRunResult) (*PipelineRunResult, error) {
	var run pipeline.RunResult
	if err := json.Unmarshal(out.Raw, &run); err != nil {
		return out, nil //nolint:nilerr // a body egress cannot read passes as it came; the run itself succeeded
	}
	shaped, err := gate.ShapeAs(call, &run)
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

// pipelineChanges are the actions that change the resource they name.
var pipelineChanges = map[string]bool{"deploy": true, "deploy_app": true, "start": true, "stop": true}

// pipelineVerbs maps a pipeline action that changes a resource to the verb it performs, whose
// plan it is bound by. health_wait only reads, and a shell action naming a resource is bound
// by its command and the resource's definition.
var pipelineVerbs = map[string]string{
	"build": verbBuild, "build_app": verbBuild,
	"deploy": localconn.OpDeploy, "deploy_app": localconn.OpDeploy,
	"start": localconn.OpApply, "stop": localconn.OpStop,
}

// pipelineTargetLookup labels a run's target with the most restrictive labels of what its
// stages change.
func pipelineTargetLookup(id string, snap *pipelineSnapshot) func(string) (*config.ResourceDef, bool) {
	return func(name string) (*config.ResourceDef, bool) {
		if name != id || snap == nil {
			return nil, false
		}
		// The pipeline is the operator's own; it takes a stricter label only from what its
		// stages change.
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

// pipelineFrozen refuses a run that would touch a frozen resource.
func (s *Service) pipelineFrozen(snap *pipelineSnapshot) error {
	if snap == nil {
		return nil
	}
	defs := map[string]config.ResourceDef{}
	for _, r := range snap.resources {
		defs[r.ID] = r
	}
	for _, stage := range snap.def.Stages {
		for _, action := range stage.Actions {
			def, ok := defs[action.Resource]
			if !ok {
				continue
			}
			if f, frozen := s.o.Gate.FrozenResource(def); frozen {
				reason := ""
				if f.Reason != "" {
					reason = ": " + f.Reason
				}
				return gate.NewError("pipeline", pipeline.OpRun, gate.CodeFrozen,
					redact.Guidance("pipeline %q does not run: its %s/%s action touches %s, which is FROZEN by %s%s. A person lifts it with `cerberus freeze --off %s`",
						snap.def.ID, stage.Name, action.Type, def.ID, f.ID, reason, f.ID))
			}
		}
	}
	return nil
}

// planPipelineSnapshot is a run's plan for a definition already read: every action of every
// stage, in order, as it would run (a shell command with its directory and the names of the
// environment variables it inherits, or the resource verb), the pipeline's definition and
// each resource it names as keyed digests, and for each action that changes a resource that
// verb's own plan (I6). It returns the snapshot it described.
func (s *Service) planPipelineSnapshot(ctx context.Context, spec gate.Spec, id string, snap *pipelineSnapshot, problem string) (plan.Plan, *pipelineSnapshot, error) {
	if snap == nil {
		return plan.Plan{}, nil, redact.Guidance("pipeline %q cannot be planned: %s", id, problem)
	}
	digest := s.o.Gate.Sink().Digest
	tgt, _ := gate.AuditTarget(spec)
	p := plan.Plan{Lane: plan.LanePipeline, Connector: spec.Connector, Operation: spec.Operation, Effect: string(spec.Op.Effect),
		Target: tgt, ArgsDigest: digest(spec.Config), Digests: map[string]string{"pipeline": digest(snap.def)}}
	resources := map[string]config.ResourceDef{}
	for _, r := range snap.resources {
		resources[r.ID] = r
	}
	lookup := func(rid string) (*config.ResourceDef, bool) {
		def, ok := resources[rid]
		return &def, ok
	}
	env := processEnvNames()
	localDef := localconn.Definition()
	for _, stage := range snap.def.Stages {
		for _, action := range stage.Actions {
			step := plan.Step{Name: stage.Name + "/" + action.Type, Command: action.Type}
			switch {
			case len(action.Argv) > 0:
				step.Command, step.Dir, step.Env = fmt.Sprintf("%q", action.Argv), action.Dir, env
			case action.Command != "":
				step.Command, step.Dir, step.Env = action.Command, action.Dir, env
			case action.Resource != "":
				step.Command = action.Type + " " + action.Resource
			}
			if action.Resource != "" {
				if def, ok := resources[action.Resource]; ok {
					p.Digests["resource:"+action.Resource] = digest(def)
				} else {
					p.Digests["resource:"+action.Resource] = "(not defined)"
				}
			}
			if verb, changes := pipelineVerbs[action.Type]; changes && action.Resource != "" {
				def, ok := lookup(action.Resource)
				if !ok || def.Type != "process" || def.Connector != "local" {
					// The run cannot resolve it either, and refuses; the digest above already
					// says so.
					p.Steps = append(p.Steps, step)
					continue
				}
				opName := verb
				if verb == verbBuild {
					opName = localconn.OpDeploy
				}
				op, known := localDef.Operation(opName)
				nested := gate.Spec{Connector: localDef.ID, Operation: action.Type, Op: op, Known: known,
					Config: map[string]any{localconn.InputID: action.Resource}, Resources: lookup}
				np, err := s.planResourceDef(ctx, nested, def, verb)
				if err != nil {
					return plan.Plan{}, nil, redact.GuidanceWrap(err, "pipeline %q cannot be planned: its %s/%s action on %q", id, stage.Name, action.Type, action.Resource)
				}
				p.Actions = append(p.Actions, np)
			}
			p.Steps = append(p.Steps, step)
		}
	}
	return p, snap, nil
}

// processEnvNames is the names of the variables in this process's environment, sorted: what a
// pipeline's shell action inherits. Names only, never values.
func processEnvNames() []string {
	var names []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// clonePipelineDefinition snapshots action argv along with stages so later config mutation
// cannot change the command whose digest the gate authorized.
func clonePipelineDefinition(def config.PipelineDef) config.PipelineDef {
	out := def
	out.Stages = append([]config.StageDef(nil), def.Stages...)
	for i := range out.Stages {
		out.Stages[i].DependsOn = append([]string(nil), def.Stages[i].DependsOn...)
		out.Stages[i].Actions = append([]config.ActionDef(nil), def.Stages[i].Actions...)
		for k := range out.Stages[i].Actions {
			out.Stages[i].Actions[k].Argv = append([]string(nil), def.Stages[i].Actions[k].Argv...)
		}
	}
	return out
}

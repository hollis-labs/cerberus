package cerbapi

import (
	"context"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// ListPipelines lists definitions from the current shared config.
func (s *ResourceRuntimeService) ListPipelines(_ context.Context) ([]PipelineInfo, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	out := make([]PipelineInfo, 0, len(cfg.Pipelines))
	for _, p := range cfg.Pipelines {
		out = append(out, PipelineInfo{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Stages:      len(p.Stages),
		})
	}
	return out, nil
}

// RunPipeline resolves and executes against the shared runtime connector.
// pipelineSnapshot is the definition a run executes: the pipeline and the
// resources it resolves against, as its plan hashed them.
type pipelineSnapshot struct {
	def       config.PipelineDef
	resources []config.ResourceDef
}

// lookupPipeline is the pipeline and the resources as the config reads now.
func (s *ResourceRuntimeService) lookupPipeline(id string) (*pipelineSnapshot, string) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, "no config available"
	}
	for i := range cfg.Pipelines {
		if cfg.Pipelines[i].ID == id {
			return &pipelineSnapshot{def: cfg.Pipelines[i], resources: append([]config.ResourceDef(nil), cfg.Resources...)}, ""
		}
	}
	return nil, fmt.Sprintf("pipeline %q not found in config", id)
}

// runPipeline runs a pipeline: the snapshot its plan was checked against
// when the gate computed one, or the config as it reads now.
func (s *ResourceRuntimeService) runPipeline(ctx context.Context, id string, checked *pipelineSnapshot, options ...MutationOption) (*PipelineRunResult, error) {
	// One gate for the whole run: the stages call the local connector
	// directly, so they are covered by this acknowledgment and never by one
	// of their own.
	if err := runtimeGate(ctx, pipeline.Definition(), pipeline.OpRun, map[string]any{"id": id}, ApplyMutationOptions(options)); err != nil {
		return nil, err
	}
	snap := checked
	if snap == nil {
		var problem string
		if snap, problem = s.lookupPipeline(id); snap == nil {
			return &PipelineRunResult{Success: false, Error: problem}, nil
		}
	}

	s.pipelineMu.Lock()
	defer s.pipelineMu.Unlock()
	p, err := pipeline.Resolve(snap.def, append([]config.ResourceDef(nil), snap.resources...), s.localConnector())
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("resolve pipeline: %s", err.Error())}, nil
	}
	env := &domain.PipelineEnv{Values: make(map[string]any)}
	exec := pipeline.NewExecutor(nil)
	// The run is the caller's; its stages are Cerberus acting for them.
	result, err := exec.Run(WithPrincipal(ctx, pipelinePrincipal(ctx, id)), p, env)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline execution: %s", err.Error())}, nil
	}
	raw, err := redact.Marshal(result)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("marshal result: %s", err.Error())}, nil
	}
	return &PipelineRunResult{Success: true, Raw: raw}, nil
}

// GetPipeline returns a definition and any resolution error without executing it.
// Invalid definitions remain inspectable; run callers can fail before printing
// that execution has started.
func (s *ResourceRuntimeService) GetPipeline(_ context.Context, id string) (*PipelineDetail, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	for _, def := range cfg.Pipelines {
		if def.ID != id {
			continue
		}
		detail := &PipelineDetail{Definition: def}
		if _, err := pipeline.Resolve(def, cfg.Resources, s.localConnector()); err != nil {
			detail.ValidationError = fmt.Sprintf("resolve pipeline: %s", err)
		}
		return detail, nil
	}
	return nil, nil
}

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
func (s *ResourceRuntimeService) RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return &PipelineRunResult{Success: false, Error: "no config available"}, nil
	}
	var pdef *config.PipelineDef
	for i := range cfg.Pipelines {
		if cfg.Pipelines[i].ID == id {
			pdef = &cfg.Pipelines[i]
			break
		}
	}
	if pdef == nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline %q not found in config", id)}, nil
	}

	s.pipelineMu.Lock()
	defer s.pipelineMu.Unlock()
	p, err := pipeline.Resolve(*pdef, append([]config.ResourceDef(nil), cfg.Resources...), s.localConnector())
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("resolve pipeline: %s", err.Error())}, nil
	}
	env := &domain.PipelineEnv{Values: make(map[string]any)}
	exec := pipeline.NewExecutor(nil)
	result, err := exec.Run(ctx, p, env)
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

package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

// StageResult captures the outcome of a single stage execution.
type StageResult struct {
	Name     string        `json:"name"`
	Status   domain.State  `json:"status"`
	Duration time.Duration `json:"duration_ms"`
	Error    string        `json:"error,omitempty"`
}

// RunResult captures the outcome of a full pipeline run.
type RunResult struct {
	PipelineID string        `json:"pipeline_id"`
	Status     domain.State  `json:"status"`
	Stages     []StageResult `json:"stages"`
	Duration   time.Duration `json:"duration_ms"`
	Error      string        `json:"error,omitempty"`
}

// Executor runs pipelines by resolving stage dependencies and executing
// actions in DAG order. Stages at the same dependency level run in parallel.
type Executor struct {
	logger *slog.Logger
}

// NewExecutor creates a pipeline executor.
func NewExecutor(logger *slog.Logger) *Executor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{logger: logger}
}

// Run executes a pipeline. Stages are run in dependency order — stages
// at the same level run concurrently. If any stage fails, subsequent
// stages that depend on it are skipped and rollback runs in reverse.
func (e *Executor) Run(ctx context.Context, p *Pipeline, env *domain.PipelineEnv) (*RunResult, error) {
	start := time.Now()

	dag, err := buildDAG(p.Stages)
	if err != nil {
		return nil, fmt.Errorf("build pipeline DAG: %w", err)
	}

	levels, err := dag.levels()
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline levels: %w", err)
	}

	result := &RunResult{
		PipelineID: p.ID,
		Status:     domain.StateRunning,
	}

	// Track completed stages for rollback
	var completed []*Stage
	var firstErr error

	for _, level := range levels {
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			break
		}

		if firstErr != nil {
			// Skip remaining levels after a failure
			for _, stage := range level {
				result.Stages = append(result.Stages, StageResult{
					Name:   stage.Name,
					Status: domain.StateStopped,
					Error:  "skipped: prior stage failed",
				})
			}
			continue
		}

		// Run all stages in this level concurrently
		stageResults := e.runLevel(ctx, level, env)

		for i, sr := range stageResults {
			result.Stages = append(result.Stages, sr)
			if sr.Status == domain.StateFailed {
				if firstErr == nil {
					firstErr = fmt.Errorf("stage %q failed: %s", sr.Name, sr.Error)
				}
			} else {
				completed = append(completed, level[i])
			}
		}
	}

	// Rollback completed stages in reverse on failure
	if firstErr != nil {
		e.rollback(ctx, completed, env)
		result.Status = domain.StateFailed
		result.Error = firstErr.Error()
	} else {
		result.Status = domain.StateHealthy
	}

	result.Duration = time.Since(start)
	return result, nil
}

// runLevel executes all stages in a level concurrently and returns their results.
func (e *Executor) runLevel(ctx context.Context, stages []*Stage, env *domain.PipelineEnv) []StageResult {
	results := make([]StageResult, len(stages))
	var wg sync.WaitGroup

	for i, stage := range stages {
		wg.Add(1)
		go func(idx int, s *Stage) {
			defer wg.Done()
			results[idx] = e.runStage(ctx, s, env)
		}(i, stage)
	}

	wg.Wait()
	return results
}

// runStage executes all actions in a stage sequentially.
func (e *Executor) runStage(ctx context.Context, s *Stage, env *domain.PipelineEnv) StageResult {
	start := time.Now()
	e.logger.Info("pipeline.stage.start", "stage", s.Name)

	for _, action := range s.Actions {
		if ctx.Err() != nil {
			return StageResult{
				Name:     s.Name,
				Status:   domain.StateFailed,
				Duration: time.Since(start),
				Error:    ctx.Err().Error(),
			}
		}

		e.logger.Info("pipeline.action.start", "stage", s.Name, "action", action.Name())

		if err := action.Execute(ctx, env); err != nil {
			e.logger.Error("pipeline.action.failed", "stage", s.Name, "action", action.Name(), "error", err)
			return StageResult{
				Name:     s.Name,
				Status:   domain.StateFailed,
				Duration: time.Since(start),
				Error:    fmt.Sprintf("action %q: %s", action.Name(), err.Error()),
			}
		}

		e.logger.Info("pipeline.action.done", "stage", s.Name, "action", action.Name())
	}

	duration := time.Since(start)
	e.logger.Info("pipeline.stage.done", "stage", s.Name, "duration", duration)

	return StageResult{
		Name:     s.Name,
		Status:   domain.StateHealthy,
		Duration: duration,
	}
}

// rollback calls Rollback on completed stages in reverse order.
// Rollback errors are logged but do not propagate.
func (e *Executor) rollback(ctx context.Context, completed []*Stage, env *domain.PipelineEnv) {
	for i := len(completed) - 1; i >= 0; i-- {
		stage := completed[i]
		for j := len(stage.Actions) - 1; j >= 0; j-- {
			action := stage.Actions[j]
			e.logger.Info("pipeline.rollback", "stage", stage.Name, "action", action.Name())
			if err := action.Rollback(ctx, env); err != nil {
				e.logger.Error("pipeline.rollback.error", "stage", stage.Name, "action", action.Name(), "error", err)
			}
		}
	}
}

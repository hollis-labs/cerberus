package domain

import (
	"context"
	"io"
)

// Action is a single unit of work within a pipeline stage.
type Action interface {
	Name() string
	Execute(ctx context.Context, env *PipelineEnv) error
	Rollback(ctx context.Context, env *PipelineEnv) error
}

// PipelineRunIO is a host-owned, admitted child environment and sanitized streams.
// It is never populated from a caller DTO.
type PipelineRunIO interface {
	Environ([]string) ([]string, error)
	Stdout() io.Writer
	Stderr() io.Writer
}

// PipelineEnv provides shared context to all actions in a pipeline run.
type PipelineEnv struct {
	RunIO     PipelineRunIO
	Resources map[string]*Resource
	Store     Store
	Secrets   SecretProvider
	Values    map[string]any // pipeline-scoped key-value store for passing data between stages
}

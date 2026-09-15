package domain

import (
	"context"
)

// Action is a single unit of work within a pipeline stage.
type Action interface {
	Name() string
	Execute(ctx context.Context, env *PipelineEnv) error
	Rollback(ctx context.Context, env *PipelineEnv) error
}

// PipelineEnv provides shared context to all actions in a pipeline run.
type PipelineEnv struct {
	Resources map[string]*Resource
	Store     Store
	Secrets   SecretProvider
	Values    map[string]any // pipeline-scoped key-value store for passing data between stages
}

package domain

import "context"

// Store persists resource, project, and pipeline state.
// The default implementation uses SQLite; the interface allows
// future adapters for PostgreSQL, etc.
type Store interface {
	// Projects
	SaveProject(ctx context.Context, p *Project) error
	GetProject(ctx context.Context, id string) (*Project, error)
	ListProjects(ctx context.Context) ([]*Project, error)
	DeleteProject(ctx context.Context, id string) error

	// Resources
	SaveResource(ctx context.Context, res *Resource) error
	GetResource(ctx context.Context, id string) (*Resource, error)
	ListResources(ctx context.Context, projectID string) ([]*Resource, error)
	DeleteResource(ctx context.Context, id string) error

	// Pipeline runs
	SavePipelineRun(ctx context.Context, run *PipelineRun) error
	GetPipelineRun(ctx context.Context, id string) (*PipelineRun, error)
	ListPipelineRuns(ctx context.Context, pipelineID string) ([]*PipelineRun, error)

	Close() error
}

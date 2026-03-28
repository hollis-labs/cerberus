package actions

import (
	"context"
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
)

// Start starts a resource via its connector.
type Start struct {
	resourceID string
	connector  domain.Connector
	resource   *domain.Resource
}

// NewStart creates a start action for the given resource.
func NewStart(resourceID string, connector domain.Connector, resource *domain.Resource) *Start {
	return &Start{resourceID: resourceID, connector: connector, resource: resource}
}

func (a *Start) Name() string { return fmt.Sprintf("start(%s)", a.resourceID) }

func (a *Start) Execute(ctx context.Context, _ *domain.PipelineEnv) error {
	return a.connector.Start(ctx, a.resource)
}

func (a *Start) Rollback(ctx context.Context, _ *domain.PipelineEnv) error {
	return a.connector.Stop(ctx, a.resource)
}

// Stop stops a resource via its connector.
type Stop struct {
	resourceID string
	connector  domain.Connector
	resource   *domain.Resource
}

// NewStop creates a stop action for the given resource.
func NewStop(resourceID string, connector domain.Connector, resource *domain.Resource) *Stop {
	return &Stop{resourceID: resourceID, connector: connector, resource: resource}
}

func (a *Stop) Name() string { return fmt.Sprintf("stop(%s)", a.resourceID) }

func (a *Stop) Execute(ctx context.Context, _ *domain.PipelineEnv) error {
	return a.connector.Stop(ctx, a.resource)
}

func (a *Stop) Rollback(ctx context.Context, _ *domain.PipelineEnv) error {
	return a.connector.Start(ctx, a.resource)
}

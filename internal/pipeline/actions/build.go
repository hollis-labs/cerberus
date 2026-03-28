package actions

import (
	"context"
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

// Build runs a service's build command synchronously.
type Build struct {
	resourceID string
	svc        *service.ManagedService
}

// NewBuild creates a build action that runs the build command for a service.
func NewBuild(resourceID string, svc *service.ManagedService) *Build {
	return &Build{resourceID: resourceID, svc: svc}
}

func (a *Build) Name() string { return fmt.Sprintf("build(%s)", a.resourceID) }

func (a *Build) Execute(_ context.Context, _ *domain.PipelineEnv) error {
	if len(a.svc.Def.Build) == 0 {
		return nil // no build command configured — skip
	}
	out, err := a.svc.BuildSync()
	if err != nil {
		return fmt.Errorf("build %s: %w\n%s", a.resourceID, err, out)
	}
	return nil
}

func (a *Build) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil // builds are not rollbackable
}

package actions

import (
	"context"
	"fmt"

	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
)

// Build runs a local process resource's build command synchronously.
type Build struct {
	resourceID string
	spec       localconn.ProcessSpec
}

// NewBuild creates a build action that runs the build command for a local process resource.
func NewBuild(resourceID string, spec localconn.ProcessSpec) *Build {
	return &Build{resourceID: resourceID, spec: spec}
}

func (a *Build) Name() string { return fmt.Sprintf("build(%s)", a.resourceID) }

func (a *Build) Execute(_ context.Context, _ *domain.PipelineEnv) error {
	if len(a.spec.Build) == 0 {
		return nil // no build command configured — skip
	}
	out, err := localconn.BuildProcess(a.spec)
	if err != nil {
		return fmt.Errorf("build %s: %w\n%s", a.resourceID, err, out)
	}
	return nil
}

func (a *Build) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil // builds are not rollbackable
}

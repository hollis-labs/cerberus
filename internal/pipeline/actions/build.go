package actions

import (
	"context"
	"fmt"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
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

func (a *Build) Execute(ctx context.Context, env *domain.PipelineEnv) error {
	if !localconn.HasBuildStrategy(a.spec) {
		return nil // no build command configured — skip
	}
	ctx, release, err := localconn.WithBuildLock(ctx, a.spec, a.resourceID)
	if err != nil {
		return err
	}
	defer release()
	result, err := localconn.BuildProcessResultContext(ctx, a.spec)
	if result == nil {
		result = &localconn.BuildResult{}
	}
	if err != nil {
		return fmt.Errorf("build %s: %w\n%s", a.resourceID, err, result.Output)
	}
	if len(result.Artifacts) > 0 {
		if env != nil {
			if env.Values == nil {
				env.Values = make(map[string]any)
			}
			env.Values[fmt.Sprintf("build.%s.artifacts", a.resourceID)] = result.Artifacts
			env.Values["build.latest.artifacts"] = result.Artifacts
		}
	}
	return nil
}

func (a *Build) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil // builds are not rollbackable
}

package actions

import (
	"context"
	"fmt"
	"strings"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
)

type localApplier interface {
	Apply(ctx context.Context, res *domain.Resource) (localconn.ApplyResult, error)
}

// Deploy builds a local process resource, optionally runs its install target,
// and activates the resource through the local runtime connector.
type Deploy struct {
	resourceID string
	resource   *domain.Resource
	spec       localconn.ProcessSpec
	local      localApplier
}

// NewDeploy creates a deploy action for a local process resource.
func NewDeploy(resourceID string, resource *domain.Resource, spec localconn.ProcessSpec, local localApplier) *Deploy {
	return &Deploy{resourceID: resourceID, resource: resource, spec: spec, local: local}
}

func (a *Deploy) Name() string { return fmt.Sprintf("deploy(%s)", a.resourceID) }

func (a *Deploy) Execute(ctx context.Context, env *domain.PipelineEnv) error {
	if a.local == nil {
		return fmt.Errorf("deploy %s: local connector is required", a.resourceID)
	}
	if guarded, ok := a.local.(interface{ ValidateMutation(*domain.Resource) error }); ok {
		if err := guarded.ValidateMutation(a.resource); err != nil {
			return err
		}
	}

	if err := localconn.ValidateDeployOutput(a.spec); err != nil {
		return err
	}
	ctx, release, lockErr := localconn.WithBuildLock(ctx, a.spec, a.resourceID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	var buildResult *localconn.BuildResult
	if localconn.HasBuildStrategy(a.spec) {
		result, err := localconn.BuildProcessResultContext(ctx, a.spec)
		if result == nil {
			result = &localconn.BuildResult{}
		}
		buildResult = result
		if err != nil {
			return fmt.Errorf("deploy %s: build failed: %w\n%s", a.resourceID, err, strings.TrimSpace(result.Output))
		}
	}

	installSkipped := false
	installOutput := ""
	if localconn.HasBuildStrategy(a.spec) && a.spec.InstallAfterBuild {
		skipped, out, err := localconn.RunInstallContext(ctx, a.spec)
		installSkipped = skipped
		installOutput = strings.TrimSpace(out)
		if err != nil {
			return fmt.Errorf("deploy %s: install failed: %w\n%s", a.resourceID, err, installOutput)
		}
	}

	activate := a.local.Apply
	if built, ok := a.local.(interface {
		ActivateBuilt(context.Context, *domain.Resource) (localconn.ApplyResult, error)
	}); ok {
		activate = built.ActivateBuilt
	}
	applyResult, err := activate(ctx, a.resource)
	if err != nil {
		return fmt.Errorf("deploy %s: apply failed: %w", a.resourceID, err)
	}

	if env != nil {
		if env.Values == nil {
			env.Values = make(map[string]any)
		}
		env.Values[fmt.Sprintf("deploy.%s.apply", a.resourceID)] = applyResult
		env.Values[fmt.Sprintf("deploy.%s.install_skipped", a.resourceID)] = installSkipped
		if installOutput != "" {
			env.Values[fmt.Sprintf("deploy.%s.install_output", a.resourceID)] = installOutput
		}
		if buildResult != nil {
			env.Values[fmt.Sprintf("deploy.%s.build_output", a.resourceID)] = buildResult.Output
			if len(buildResult.Artifacts) > 0 {
				env.Values[fmt.Sprintf("build.%s.artifacts", a.resourceID)] = buildResult.Artifacts
				env.Values["build.latest.artifacts"] = buildResult.Artifacts
			}
		}
	}
	return nil
}

func (a *Deploy) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil
}

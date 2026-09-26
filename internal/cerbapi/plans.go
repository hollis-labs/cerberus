package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// The plan functions: one per lane, used when an approval is asked for and
// again when it is used, so the two cannot be computed differently (I6).

// planOperation is the admin lane's plan: the operation, its resolved target
// and labels, the keyed digest of its arguments, its dry-run preview where
// it has one, and for a plugin which binary and config would run.
func (s *ExternalConnectorService) planOperation(ctx context.Context, args ExternalConnectorOperationArgs) (plan.Plan, error) {
	op, err := s.declaredOperation(ctx, args)
	if err != nil {
		return plan.Plan{}, err
	}
	spec := s.auditSpec(args)
	tgt, _ := auditTarget(spec)
	p := plan.Plan{Lane: plan.LaneAdmin, Connector: args.Connector, Operation: args.Operation, Effect: string(op.Effect),
		Target: tgt, ArgsDigest: s.audit.Digest(args.Config)}
	if s.managedPlugins != nil && s.managedPlugins.Loaded(args.Connector) {
		if perr := s.managedPlugins.planPlugin(ctx, &p, op, args.Connector, args.Config, args.Acknowledged); perr != nil {
			return plan.Plan{}, perr
		}
		return p, nil
	}
	resolved := args
	switch args.Connector {
	case "ssh":
		if resolved, err = s.resolveSSHTarget(args); err != nil {
			return plan.Plan{}, err
		}
	case "docker":
		if resolved, err = s.resolveDockerResource(args); err != nil {
			return plan.Plan{}, err
		}
	}
	if op.Preview != contract.PreviewNone {
		if preview, ok, perr := s.dryRunPreview(resolved); perr != nil {
			return plan.Plan{}, perr
		} else if ok {
			if err := p.WithPreview(string(op.Preview), preview); err != nil {
				return plan.Plan{}, err
			}
		}
	}
	return p, nil
}

// ConnectorPlan is a plan shown on request: what an approval of the call
// would bind to, and the hash it would record.
type ConnectorPlan struct {
	PlanHash string    `json:"plan_hash"`
	Plan     plan.Plan `json:"plan"`
}

// showPlan answers a plan request with the lane's own plan function — the
// one an approval is asked for and used with — so what is shown is what
// would be bound. It runs nothing but the preview.
func (s *ExternalConnectorService) showPlan(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	p, err := s.planOperation(ctx, args)
	if err != nil {
		var coded *ExternalConnectorError
		if errors.As(err, &coded) {
			return ExternalConnectorOperationResult{}, err
		}
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	p.V = plan.Version
	hash, err := p.Hash()
	if err != nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	return ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: ConnectorPlan{PlanHash: hash, Plan: p}}, nil
}

// planPlugin adds a plugin's part of a plan: its fingerprints, and its own
// dry-run preview where the operation declares one. Running the preview is
// a real call to the plugin — the provider's own answer to "would this be
// accepted", with any version token it carries — which is why a plan is
// computed only when an approval is asked for or used, or when the
// operator asks to see one.
func (s *ManagedPluginConnectorService) planPlugin(ctx context.Context, p *plan.Plan, op contract.Operation, id string, config map[string]any, acknowledged bool) error {
	p.PluginConfigSHA256, p.PluginEntrypointSHA256 = s.fingerprints(id)
	if op.Preview == contract.PreviewNone {
		return nil
	}
	res, err := s.execute(ctx, id, PluginConnectorExecArgs{Operation: op.Name, Config: config, DryRun: true, Acknowledged: acknowledged})
	if err != nil {
		return fmt.Errorf("the plugin's preview, which the plan needs, failed: %w", err)
	}
	return p.WithPreview(string(op.Preview), res.Data)
}

// planDeploymentProfile is a deploy profile's plan: the steps it would run
// as shown (the token as a placeholder, reaching the child only in its
// environment), the profile's definition, and the checkout it deploys —
// its commit and whether it has uncommitted changes. An edited profile, a
// new commit or a dirty tree is a different plan (CERB-GAP-853).
func planDeploymentProfile(ctx context.Context, spec auditSpec, sink interface{ Digest(any) string }, secrets secret.Provider, profile infra.DeploymentProfile) (plan.Plan, error) {
	tgt, _ := auditTarget(spec)
	p := plan.Plan{Lane: plan.LaneDeployProfile, Connector: spec.connector, Operation: spec.operation, Effect: string(spec.op.Effect),
		Target: tgt, ArgsDigest: sink.Digest(spec.config)}
	dp := infra.PlanDeployment(ctx, secrets, profile)
	if dp.Error != "" {
		return plan.Plan{}, redact.Guidance("deploy profile %q cannot be planned: %s", profile.ID, dp.Error)
	}
	for _, step := range dp.Steps {
		p.Steps = append(p.Steps, plan.Step{Name: step.Name, Command: step.Command, Dir: profile.RepoPath, Env: step.Env})
	}
	def, err := plan.Canonical(profile)
	if err != nil {
		return plan.Plan{}, err
	}
	sum := sha256.Sum256(def)
	p.Digests = map[string]string{"profile": "sha256:" + hex.EncodeToString(sum[:])}
	p.Source = gitSource(ctx, profile.RepoPath)
	return p, nil
}

// gitSource is a checkout's commit and dirty flag. A directory that is not a
// repository, or a git that cannot be found, is recorded as such rather
// than failing the plan: the plan still binds everything else.
func gitSource(ctx context.Context, dir string) *plan.Source {
	src := &plan.Source{Path: dir}
	git := gitBinary()
	if git == "" {
		src.HEAD = "(git not found)"
		return src
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, git, append([]string{"-C", dir}, args...)...) //nolint:gosec // a resolved git binary on the profile's own checkout
		cmd.Env = gitEnv()
		return cmd.Output()
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		src.HEAD = "(not a git checkout)"
		return src
	}
	src.HEAD = strings.TrimSpace(string(head))
	status, err := run("status", "--porcelain")
	src.Dirty = err != nil || len(strings.TrimSpace(string(status))) > 0
	return src
}

// gitEnv is the process environment without the variables that point git
// at a repository. A caller running inside a git hook has GIT_DIR set, and
// `git -C dir` would read that repository instead of dir.
func gitEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX":
			continue
		}
		env = append(env, entry)
	}
	return env
}

// gitBinary resolves git per call: the daemon's PATH is launchd's minimal
// one, so a PATH lookup alone is not enough.
func gitBinary() string {
	if path, err := exec.LookPath("git"); err == nil {
		return path
	}
	for _, candidate := range []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

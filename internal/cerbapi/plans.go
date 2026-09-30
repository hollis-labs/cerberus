package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/gitenv"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
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
	spec := s.auditSpecIn(ctx, args)
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
		if resolved, err = s.resolveSSHTarget(ctx, args); err != nil {
			return plan.Plan{}, err
		}
	case "docker":
		if resolved, err = s.resolveDockerResource(ctx, args); err != nil {
			return plan.Plan{}, err
		}
	}
	if args.Connector == "ssh" || args.Connector == "docker" {
		// The target as it would run, the configured resource merged in,
		// keyed like the arguments (M10): an approval does not outlive an
		// edit to the host, key, context or compose file it was for.
		if p.Digests == nil {
			p.Digests = map[string]string{}
		}
		p.Digests["target"] = s.audit.Digest(resolved.Config)
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
	// ComputedBy is the surface of the process that computed the plan:
	// socket (the daemon), in_process (the CLI's own) or web. It is not part
	// of the plan: the same call can plan differently in different
	// processes, since a pipeline's shell steps list the environment of the
	// process that runs them (CERB-GAP-882).
	ComputedBy CallerSurface `json:"computed_by"`
}

// showPlan answers a plan request with the lane's own plan function — the
// one an approval is asked for and used with — so what is shown is what
// would be bound. It runs nothing but the preview.
func (s *ExternalConnectorService) showPlan(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	shown, err := showPlan(ctx, s.auditSpecIn(ctx, args))
	if err != nil {
		return ExternalConnectorOperationResult{}, err
	}
	return ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: *shown}, nil
}

// showPlan computes a spec's plan through its lane's plan function and
// hashes it, for a caller that asked to see it.
func showPlan(ctx context.Context, spec auditSpec) (*ConnectorPlan, error) {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	if spec.plan == nil {
		return nil, externalConnectorError(args, ExternalConnectorUnsupported, redact.Guidance("%s %s has no plan to show", spec.connector, spec.operation))
	}
	p, err := spec.plan(ctx)
	if err != nil {
		var coded *ExternalConnectorError
		if errors.As(err, &coded) {
			return nil, err
		}
		return nil, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	p.V = plan.Version
	hash, err := p.Hash()
	if err != nil {
		return nil, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	return &ConnectorPlan{PlanHash: hash, Plan: p, ComputedBy: CallerSurfaceFrom(ctx)}, nil
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

// gitSource is a checkout's commit and dirty flag. A directory that is not a
// repository, or a git that cannot be found, is recorded as such rather
// than failing the plan: the plan still binds everything else.
func gitSource(ctx context.Context, dir string) *plan.Source {
	src := &plan.Source{Path: dir}
	if _, err := exec.LookPath(gitenv.Binary()); err != nil {
		src.HEAD = "(git not found)"
		return src
	}
	run := func(args ...string) ([]byte, error) { return gitenv.Command(ctx, dir, args...).Output() }
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		src.HEAD = "(not a git checkout)"
		return src
	}
	src.HEAD = strings.TrimSpace(string(head))
	status, err := run("status", "--porcelain")
	src.Dirty = err != nil || len(strings.TrimSpace(string(status))) > 0
	if src.Dirty {
		src.Content = dirtyContent(ctx, dir)
	}
	return src
}

// sourceKey carries the checkout a deploy was checked against: its commit
// and uncommitted content, read once at the gate, so the plan hashes it and
// the build refuses a tree that changed since (M10).
type sourceKey struct{}

type checkedCheckout struct {
	dir string
	src *plan.Source
}

// withCheckedSource is ctx carrying dir's checkout as read now.
func withCheckedSource(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, sourceKey{}, checkedCheckout{dir: dir, src: gitSource(ctx, dir)})
}

// checkedSource is dir's checkout as the call's gate read it, or as it is
// now outside such a call.
func checkedSource(ctx context.Context, dir string) *plan.Source {
	if c, ok := ctx.Value(sourceKey{}).(checkedCheckout); ok && c.dir == dir && c.src != nil {
		src := *c.src
		return &src
	}
	return gitSource(ctx, dir)
}

// sourceChanged says how dir's checkout differs from what the call's gate
// read, or "" when it does not, or no gate read it.
func sourceChanged(ctx context.Context, dir string) string {
	c, ok := ctx.Value(sourceKey{}).(checkedCheckout)
	if !ok || c.dir != dir || c.src == nil {
		return ""
	}
	now := gitSource(ctx, dir)
	if now.HEAD == c.src.HEAD && now.Dirty == c.src.Dirty && now.Content == c.src.Content {
		return ""
	}
	describe := func(s *plan.Source) string {
		out := s.HEAD
		if len(out) > 12 {
			out = out[:12]
		}
		if s.Dirty {
			out += " with uncommitted changes"
		}
		return out
	}
	return fmt.Sprintf("the checkout at %s changed after this deploy was checked (it was %s, it is %s), so nothing was built; run the deploy again, and it is checked as the tree is now", dir, describe(c.src), describe(now))
}

// dirtyContent is a digest of a checkout's uncommitted changes: its diff
// against HEAD, and each untracked file that is not ignored, by path and
// content. It reads the whole repository, not only dir, since a build in a
// subdirectory can reach the rest of it.
func dirtyContent(ctx context.Context, dir string) string {
	run := func(args ...string) ([]byte, error) { return gitenv.Command(ctx, dir, args...).Output() }
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return "(unreadable)"
	}
	root := strings.TrimSpace(string(top))
	h := sha256.New()
	diff, err := gitenv.Command(ctx, root, "diff", "HEAD", "--binary").Output()
	if err != nil {
		return "(unreadable)"
	}
	h.Write(diff)
	untracked, err := gitenv.Command(ctx, root, "ls-files", "-o", "--exclude-standard", "-z").Output()
	if err != nil {
		return "(unreadable)"
	}
	paths := strings.Split(strings.TrimRight(string(untracked), "\x00"), "\x00")
	sort.Strings(paths)
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		h.Write([]byte("\x00untracked\x00" + rel + "\x00"))
		h.Write([]byte(fileDigest(filepath.Join(root, rel))))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// planResource is a resource verb's plan: the resource's definition (as a
// keyed digest, since its environment can carry values), what the verb
// would install and from where, and the state it would change.
//   - deploy builds: the checkout's commit and dirty flag, and the build
//     output it would install.
//   - apply and sync install the build output as it is now: its path and
//     sha256.
//   - apply and deploy write a launch agent: the rendered plist, as a keyed
//     digest.
//   - every verb: the observed state, so an approval to stop a running
//     service does not stop it after it was restarted as something else.
func (s *ResourceRuntimeService) planResource(ctx context.Context, spec auditSpec, id string) (plan.Plan, error) {
	res, err := s.requireLocalProcessResource(ctx, id)
	if err != nil {
		return plan.Plan{}, err
	}
	return s.planResourceDef(ctx, spec, res, spec.operation)
}

// verbBuild is a pipeline's build action: deploy's build half, which binds
// the checkout and the output path and installs nothing.
const verbBuild = "build"

// planResourceDef is planResource for a definition in hand, as a pipeline's
// checked snapshot holds it, planning what verb would do: one of the local
// connector's operations, or verbBuild.
func (s *ResourceRuntimeService) planResourceDef(ctx context.Context, spec auditSpec, res *config.ResourceDef, verb string) (plan.Plan, error) {
	pspec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return plan.Plan{}, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}
	id := res.ID
	tgt, _ := auditTarget(spec)
	p := plan.Plan{Lane: plan.LaneResource, Connector: spec.connector, Operation: spec.operation, Effect: string(spec.op.Effect),
		Target: tgt, ArgsDigest: s.audit.Digest(spec.config), Digests: map[string]string{"spec": s.audit.Digest(res)}}
	builds := verb == localconn.OpDeploy || verb == verbBuild
	installs := builds || verb == localconn.OpApply || verb == localconn.OpSync
	if builds && localconn.HasBuildStrategy(pspec) {
		p.Source = checkedSource(ctx, pspec.Dir)
	}
	if installs && pspec.RunFrom == localconn.ProcessRunFromArtifact {
		if path, perr := localconn.ResolveArtifactSourcePath(pspec); perr == nil {
			p.Artifact = path
			// A build comes before the install, so the output as it is now
			// is not what would be installed; the source binds that instead.
			if !builds {
				p.Digests["artifact"] = fileDigest(path)
			}
		} else {
			p.Artifact = "(unresolved: " + perr.Error() + ")"
		}
	}
	dr := resourceDefToDomain(res)
	if verb == localconn.OpDeploy || verb == localconn.OpApply {
		if rendered, ok, perr := s.localConnector().PreviewPlist(dr); perr != nil {
			return plan.Plan{}, fmt.Errorf("render the launch agent %q would install: %w", id, perr)
		} else if ok {
			p.Digests["plist"] = s.audit.Digest(string(rendered))
		}
		if rendered, ok, perr := s.localConnector().PreviewSystemdUnit(dr); perr != nil {
			return plan.Plan{}, fmt.Errorf("render the systemd unit %q would install: %w", id, perr)
		} else if ok {
			p.Digests["unit"] = s.audit.Digest(string(rendered))
		}
	}
	state, err := s.statusWithTimeout(ctx, dr)
	if err != nil {
		p.State = "unknown: " + err.Error()
	} else {
		p.State = string(state)
	}
	return p, nil
}

// processEnvNames is the names of the variables in this process's
// environment, sorted: what a pipeline's shell action inherits. Names only,
// never values, so an approver can see a PATH or LD_* surprise.
func processEnvNames() []string {
	var names []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// fileDigest is a file's sha256, or why it has none.
func fileDigest(path string) string {
	f, err := os.Open(path) //nolint:gosec // a resource's own build output
	if err != nil {
		return "(missing)"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "(unreadable)"
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// pipelineVerbs maps a pipeline action that changes a resource to the verb
// it performs, whose plan it is bound by. health_wait only reads, and a
// shell action naming a resource is bound by its command and the
// resource's definition.
var pipelineVerbs = map[string]string{
	"build": verbBuild, "build_app": verbBuild,
	"deploy": localconn.OpDeploy, "deploy_app": localconn.OpDeploy,
	"start": localconn.OpApply, "stop": localconn.OpStop,
}

// planPipeline is a pipeline run's plan: every action of every stage, in
// order, as it would run (a shell command with its directory and the names
// of the environment variables it inherits, or the resource verb), the
// pipeline's definition and the definition of each resource it names, both
// as keyed digests, and for each action that changes a resource, that
// verb's own plan, as `cerberus resource plan` would compute it (I6: a
// pipeline that deploys a resource binds that deploy). It returns the
// snapshot it described, so a run checked against an approval runs that
// snapshot.
func (s *ResourceRuntimeService) planPipeline(ctx context.Context, spec auditSpec, id string) (plan.Plan, *pipelineSnapshot, error) {
	snap, problem := s.lookupPipeline(id)
	return s.planPipelineSnapshot(ctx, spec, id, snap, problem)
}

// planPipelineSnapshot is planPipeline for a definition already read, so a
// run's gate labels, plan and stages all come from one read (M10).
func (s *ResourceRuntimeService) planPipelineSnapshot(ctx context.Context, spec auditSpec, id string, snap *pipelineSnapshot, problem string) (plan.Plan, *pipelineSnapshot, error) {
	if snap == nil {
		return plan.Plan{}, nil, redact.Guidance("pipeline %q cannot be planned: %s", id, problem)
	}
	tgt, _ := auditTarget(spec)
	p := plan.Plan{Lane: plan.LanePipeline, Connector: spec.connector, Operation: spec.operation, Effect: string(spec.op.Effect),
		Target: tgt, ArgsDigest: s.audit.Digest(spec.config), Digests: map[string]string{"pipeline": s.audit.Digest(snap.def)}}
	resources := map[string]config.ResourceDef{}
	for _, r := range snap.resources {
		resources[r.ID] = r
	}
	lookup := func(rid string) (*config.ResourceDef, bool) {
		def, ok := resources[rid]
		return &def, ok
	}
	env := processEnvNames()
	localDef := localconn.Definition()
	for _, stage := range snap.def.Stages {
		for _, action := range stage.Actions {
			step := plan.Step{Name: stage.Name + "/" + action.Type, Command: action.Type}
			switch {
			case action.Command != "":
				step.Command, step.Dir, step.Env = action.Command, action.Dir, env
			case action.Resource != "":
				step.Command = action.Type + " " + action.Resource
			}
			if action.Resource != "" {
				if def, ok := resources[action.Resource]; ok {
					p.Digests["resource:"+action.Resource] = s.audit.Digest(def)
				} else {
					p.Digests["resource:"+action.Resource] = "(not defined)"
				}
			}
			if verb, changes := pipelineVerbs[action.Type]; changes && action.Resource != "" {
				def, ok := lookup(action.Resource)
				if !ok || def.Type != "process" || def.Connector != "local" {
					// The run cannot resolve it either, and refuses; the digest
					// above already says so.
					p.Steps = append(p.Steps, step)
					continue
				}
				opName := verb
				if verb == verbBuild {
					opName = localconn.OpDeploy
				}
				op, known := localDef.Operation(opName)
				nested := auditSpec{connector: localDef.ID, operation: action.Type, op: op, known: known,
					config: map[string]any{localconn.InputID: action.Resource}, resources: lookup}
				np, err := s.planResourceDef(ctx, nested, def, verb)
				if err != nil {
					return plan.Plan{}, nil, redact.GuidanceWrap(err, "pipeline %q cannot be planned: its %s/%s action on %q", id, stage.Name, action.Type, action.Resource)
				}
				p.Actions = append(p.Actions, np)
			}
			p.Steps = append(p.Steps, step)
		}
	}
	return p, snap, nil
}

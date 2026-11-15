package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// OpResult is a mutation's result in the old build's shape (cerbapi OpResult). A failure after
// the gate is Success false with Error, never an error: the API answers it with 200.
type OpResult struct {
	Warnings       []string                      `json:"warnings,omitempty"`
	BuildPerformed bool                          `json:"build_performed"`
	Activation     *localconn.ActivationArtifact `json:"activation,omitempty"`
	Success        bool                          `json:"success"`
	ServiceID      string                        `json:"service_id"`
	Message        string                        `json:"message,omitempty"`
	BuildOutput    string                        `json:"build_output,omitempty"`
	BuildLogPath   string                        `json:"build_log_path,omitempty"`
	InstallOutput  string                        `json:"install_output,omitempty"`
	InstallSkipped bool                          `json:"install_skipped,omitempty"`
	Error          string                        `json:"error,omitempty"`
}

// Digester keys an applied definition's digest. The Substrate build uses the audit chain's
// digest key, copied from the old tree, so a digest migrated from runtime/applied.json compares
// equal (docs/substrate-port-plan.md, section 6).
type Digester interface {
	Digest(v any) string
}

// appliedVerbs record the definition they ran, as in the old build (cerbapi appliedVerbs).
var appliedVerbs = map[string]bool{localconn.OpApply: true, localconn.OpDeploy: true, localconn.OpReload: true, localconn.OpSync: true}

// failed is the old build's post-gate failure: 200 with success false.
func failed(id string, err error) *OpResult {
	return &OpResult{Success: false, ServiceID: id, Error: err.Error()}
}

// Mutate runs one verb. A refusal before the run (observe mode, acknowledgment, approval) is
// an error, a *Refusal for the gate; everything after it is an OpResult, with Success false
// when the run failed. In supervise mode the order is the old build's: the acknowledgment,
// then the approver, then the resource.
func (s *Service) Mutate(ctx context.Context, req Request) (*OpResult, error) {
	if _, ok := operation(req.Verb); !ok {
		return nil, requireAck(req)
	}
	// Observe mode refuses before anything else (legacy-routes.md: L7 and L8 answer 409
	// observe_mode until the handover).
	if s.o.Mode != settings.ModeSupervise {
		return nil, s.observeRefusal(req.Verb, req.ID)
	}
	if err := requireAck(req); err != nil {
		return nil, err
	}
	cfg, err := s.config()
	if err != nil {
		return failed(req.ID, err), nil
	}
	res := findResource(cfg, req.ID)
	if err := s.o.Approver.Approve(ctx, req, res); err != nil {
		return nil, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var out *OpResult
	switch req.Verb {
	case localconn.OpReload:
		out = s.reload(ctx, cfg, req.ID)
	case localconn.OpStop:
		out = s.stop(ctx, cfg, req.ID)
	case localconn.OpDeploy:
		out = s.deploy(ctx, cfg, req)
	case localconn.OpApply:
		out = s.apply(ctx, cfg, req.ID)
	case localconn.OpSync:
		// The old build's sync answers a canceled call with the error, not a result.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = s.sync(cfg, req.ID)
	case localconn.OpRemove:
		out = s.remove(ctx, cfg, req.ID)
	default:
		return nil, fmt.Errorf("unknown resource verb %q", req.Verb)
	}
	if out.Success && appliedVerbs[req.Verb] && res != nil {
		if err := storage.SetApplied(ctx, s.o.Store, req.ID, s.o.Digester.Digest(*res), time.Now()); err != nil {
			s.o.Logger.Error("supervise.applied_record_failed", "resource", req.ID, "error", err.Error())
		}
	}
	return out, nil
}

func (s *Service) config() (*config.ConfigV2, error) {
	cfg, err := s.o.Config()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("no config available")
	}
	return cfg, nil
}

func findResource(cfg *config.ConfigV2, id string) *config.ResourceDef {
	for i := range cfg.Resources {
		if cfg.Resources[i].ID == id {
			return &cfg.Resources[i]
		}
	}
	return nil
}

// requireLocalProcess is the old build's requireLocalProcessResource and spec decode. res is
// nil when the lookup failed and set when only the spec did not decode, because the old
// build's deploy and apply add their warnings only once the resource was found.
func requireLocalProcess(cfg *config.ConfigV2, id string) (*config.ResourceDef, localconn.ProcessSpec, error) {
	res := findResource(cfg, id)
	if res == nil {
		return nil, localconn.ProcessSpec{}, fmt.Errorf("resource %q not found", id)
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return nil, localconn.ProcessSpec{}, fmt.Errorf("resource %q is %s/%s; operation currently supports local process resources only", res.ID, res.Type, res.Connector)
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return nil, localconn.ProcessSpec{}, err
	}
	return res, spec, nil
}

func refusePortConflict(cfg *config.ConfigV2, id string) error {
	for _, conflict := range registry.PortConflicts(cfg.Resources) {
		for _, r := range conflict.Resources {
			if r.ID == id {
				return errors.New(conflict.String())
			}
		}
	}
	return nil
}

func isDevSession(spec localconn.ProcessSpec) bool {
	return spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession
}

// pause records operator intent for a dev session (pause state, store class A).
func (s *Service) pause(ctx context.Context, id string, paused bool) error {
	return storage.SetPaused(ctx, s.o.Store, id, paused, time.Now())
}

func (s *Service) reload(ctx context.Context, cfg *config.ConfigV2, id string) *OpResult {
	res, spec, err := requireLocalProcess(cfg, id)
	if err != nil {
		return failed(id, err)
	}
	if err := refusePortConflict(cfg, id); err != nil {
		return failed(id, err)
	}
	if err := s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	if isDevSession(spec) {
		_ = s.pause(ctx, id, false)
	}
	if err := s.conn.Reload(ctx, ResourceToDomain(res)); err != nil {
		return failed(id, err)
	}
	return &OpResult{Success: true, ServiceID: id, Message: fmt.Sprintf("resource %q reloaded successfully", id)}
}

func (s *Service) stop(ctx context.Context, cfg *config.ConfigV2, id string) *OpResult {
	res, spec, err := requireLocalProcess(cfg, id)
	if err != nil {
		return failed(id, err)
	}
	if err := s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	paused := false
	if isDevSession(spec) {
		if err := s.pause(ctx, id, true); err != nil {
			return failed(id, err)
		}
		paused = true
	}
	if err := s.conn.Stop(ctx, ResourceToDomain(res)); err != nil {
		if paused {
			_ = s.pause(ctx, id, false)
		}
		return failed(id, err)
	}
	return &OpResult{Success: true, ServiceID: id, Message: fmt.Sprintf("resource %q stopped successfully; install state preserved", id)}
}

// finish adds what the old build's deploy and apply add to every result: the config and
// dependency warnings, and the output redacted through the resource's own redactor.
func (s *Service) finish(ctx context.Context, cfg *config.ConfigV2, res *config.ResourceDef, out *OpResult) *OpResult {
	out.Warnings = append(out.Warnings, localconn.ProcessConfigWarnings(res.Config)...)
	out.Warnings = append(out.Warnings, s.dependencyWarnings(ctx, cfg, res)...)
	if spec, err := localconn.SpecFromResourceConfig(res.Config); err == nil {
		r := localconn.OutputRedactor(spec)
		out.Message = r.Text(out.Message)
		out.Error = r.Text(out.Error)
		out.BuildOutput = r.Text(out.BuildOutput)
		out.InstallOutput = r.Text(out.InstallOutput)
	}
	return out
}

func (s *Service) deploy(ctx context.Context, cfg *config.ConfigV2, req Request) *OpResult {
	id := req.ID
	res, spec, err := requireLocalProcess(cfg, id)
	if res == nil {
		return failed(id, err)
	}
	if err != nil {
		return s.finish(ctx, cfg, res, failed(id, err))
	}
	return s.finish(ctx, cfg, res, s.deployRun(ctx, cfg, res, spec, req))
}

func (s *Service) deployRun(ctx context.Context, cfg *config.ConfigV2, res *config.ResourceDef, spec localconn.ProcessSpec, req Request) *OpResult {
	id := res.ID
	if err := refusePortConflict(cfg, id); err != nil {
		return failed(id, err)
	}
	if err := s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	if err := localconn.ValidateDeployOutput(spec); err != nil {
		return failed(id, err)
	}
	installAfterBuild := ResolveInstallAfterBuild(res.Config, spec.InstallAfterBuild, cfg.InstallAfterBuildDefault(), req.InstallAfterBuildOverride)
	ctx, release, err := localconn.WithBuildLock(ctx, spec, id)
	if err != nil {
		return failed(id, err)
	}
	defer release()
	dr := ResourceToDomain(res)
	out := &OpResult{ServiceID: id}
	if localconn.HasBuildStrategy(spec) {
		result, buildErr := localconn.BuildProcessResultContext(ctx, spec)
		if result == nil {
			// An unknown build_strategy kind: fail with buildErr, never panic.
			result = &localconn.BuildResult{}
		}
		out.BuildOutput = strings.TrimSpace(result.Output)
		if p, logErr := localconn.WriteBuildLog(dr, spec, result, buildErr); logErr == nil {
			out.BuildLogPath = p
		}
		if buildErr != nil {
			out.Error = fmt.Sprintf("build failed for resource %q (%s): %s", id, localconn.BuildCommandSummary(spec, result), buildErr.Error())
			if out.BuildLogPath != "" {
				out.Error += fmt.Sprintf(" — see %s", out.BuildLogPath)
			}
			return out
		}
		if installAfterBuild {
			skipped, instOut, installErr := localconn.RunInstallContext(ctx, spec)
			out.InstallOutput = strings.TrimSpace(instOut)
			if installErr != nil {
				out.Error = fmt.Sprintf("install failed for resource %q: %s", id, installErr.Error())
				return out
			}
			out.InstallSkipped = skipped
			if skipped {
				s.o.Logger.Info("resource_runtime.install.skipped", "resource", id, "reason", "no_install_target_in_makefile")
			}
		}
		// The build must have produced the binary an artifact-mode resource runs.
		if spec.RunFrom == localconn.ProcessRunFromArtifact {
			if src, srcErr := localconn.ResolveArtifactSourcePath(spec); srcErr == nil {
				if _, statErr := os.Stat(src); statErr != nil {
					out.Error = fmt.Sprintf(
						"build for %q completed but produced no artifact at %s; the build_strategy output does not match what the service runs (command[0]) — set the build_strategy `output` rule to the built binary",
						id, src)
					out.InstallOutput, out.InstallSkipped = "", false
					return out
				}
			}
		}
	}
	if isDevSession(spec) {
		_ = s.pause(ctx, id, false)
	}
	applyRes, applyErr := s.conn.ActivateBuilt(ctx, dr)
	if applyErr != nil {
		out.BuildLogPath = ""
		out.Error = s.formatApplyError(id, res, spec, applyErr)
		return out
	}
	out.Activation, _ = localconn.InspectActivationArtifact(dr, spec)
	msg := localconn.FormatApplyResultMessage(id, spec, applyRes)
	if localconn.HasBuildStrategy(spec) {
		msg = fmt.Sprintf("resource %q deployed successfully (build completed; runtime %s)", id, applyRes.Action)
	} else {
		msg += "; no build_strategy configured, activated existing output"
	}
	out.Success, out.BuildPerformed, out.Message = true, localconn.HasBuildStrategy(spec), msg
	return out
}

func (s *Service) apply(ctx context.Context, cfg *config.ConfigV2, id string) *OpResult {
	res, spec, err := requireLocalProcess(cfg, id)
	if res == nil {
		return failed(id, err)
	}
	if err != nil {
		return s.finish(ctx, cfg, res, failed(id, err))
	}
	return s.finish(ctx, cfg, res, s.applyRun(ctx, cfg, res, spec))
}

func (s *Service) applyRun(ctx context.Context, cfg *config.ConfigV2, res *config.ResourceDef, spec localconn.ProcessSpec) *OpResult {
	id := res.ID
	if err := refusePortConflict(cfg, id); err != nil {
		return failed(id, err)
	}
	if err := s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	if isDevSession(spec) {
		_ = s.pause(ctx, id, false)
	}
	dr := ResourceToDomain(res)
	applyRes, err := s.conn.Apply(ctx, dr)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: s.formatApplyError(id, res, spec, err)}
	}
	activation, _ := localconn.InspectActivationArtifact(dr, spec)
	return &OpResult{
		Activation: activation,
		Success:    true,
		ServiceID:  id,
		Message: localconn.FormatApplyResultMessage(id, spec, applyRes) +
			"; no build ran and source freshness was not checked; use cerberus resource deploy " + id + " --ack after source changes",
	}
}

func (s *Service) formatApplyError(id string, res *config.ResourceDef, spec localconn.ProcessSpec, err error) string {
	msg := strings.TrimSpace(err.Error())
	inspectHint := fmt.Sprintf("run `cerberus resource doctor %s` for a full runtime/install check", id)
	if spec.Mode != localconn.ProcessModeOSService {
		return fmt.Sprintf("%s; %s", msg, inspectHint)
	}
	layout, layoutErr := localconn.DefaultInstallLayout(s.o.Home, ResourceToDomain(res), spec)
	if layoutErr != nil {
		return fmt.Sprintf("%s; %s", msg, inspectHint)
	}
	definition := "plist: " + layout.PlistPath
	if localconn.IsSystemdSupervised(spec) {
		definition = "unit: " + layout.UnitPath
	}
	return fmt.Sprintf("%s; stderr log: %s; stdout log: %s; %s; install: %s; artifact: %s; %s",
		msg,
		filepath.Join(layout.RootDir, "logs", "stderr.log"),
		filepath.Join(layout.RootDir, "logs", "stdout.log"),
		definition,
		layout.RootDir,
		layout.ArtifactPath,
		inspectHint,
	)
}

func (s *Service) sync(cfg *config.ConfigV2, id string) *OpResult {
	res, spec, err := requireLocalProcess(cfg, id)
	if err != nil {
		return failed(id, err)
	}
	if err = s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	if spec.RunFrom != localconn.ProcessRunFromArtifact {
		return &OpResult{Success: true, ServiceID: id, Message: fmt.Sprintf("resource %q does not use artifact mode; nothing to sync", id)}
	}
	_, syncRes, err := localconn.SyncArtifactInstall(ResourceToDomain(res), spec)
	if err != nil {
		return failed(id, err)
	}
	msg := fmt.Sprintf("resource %q artifact already current", id)
	if syncRes.Changed {
		msg = fmt.Sprintf("resource %q artifact synced", id)
	}
	return &OpResult{Success: true, ServiceID: id, Message: msg}
}

func (s *Service) remove(ctx context.Context, cfg *config.ConfigV2, id string) *OpResult {
	res, spec, err := requireLocalProcess(cfg, id)
	if err != nil {
		return failed(id, err)
	}
	if err := s.refuseSelf(res); err != nil {
		return failed(id, err)
	}
	if isDevSession(spec) {
		_ = s.pause(ctx, id, true)
	}
	if err := s.conn.Destroy(ctx, ResourceToDomain(res)); err != nil {
		return failed(id, err)
	}
	return &OpResult{Success: true, ServiceID: id, Message: fmt.Sprintf("resource %q removed successfully", id)}
}

// ResolveInstallAfterBuild is the old build's precedence: the caller's override, then the
// resource's own key when present, then the global default.
func ResolveInstallAfterBuild(rawCfg map[string]any, resourceVal, globalDefault bool, override *bool) bool {
	if override != nil {
		return *override
	}
	if _, present := rawCfg["install_after_build"]; present {
		return resourceVal
	}
	return globalDefault
}

const statusProbeTimeout = 750 * time.Millisecond

// dependencyWarnings are advisory, as in the old build: an explicit operation runs, with each
// unavailable dependency named. Nothing else is started.
func (s *Service) dependencyWarnings(ctx context.Context, cfg *config.ConfigV2, res *config.ResourceDef) []string {
	if len(res.DependsOn) == 0 {
		return nil
	}
	warnings := dependencyCycles(cfg.Resources, res.ID)
	for _, dep := range res.DependsOn {
		d := findResource(cfg, dep)
		if d == nil {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q, which is not configured", res.ID, dep))
			continue
		}
		if d.Connector != "local" || d.Type != "process" {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q (%s/%s); availability was not checked", res.ID, dep, d.Connector, d.Type))
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, statusProbeTimeout)
		state, err := s.conn.Status(probeCtx, ResourceToDomain(d))
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q; availability check failed: %v", res.ID, dep, err))
			continue
		}
		if state != domain.StateRunning && state != domain.StateHealthy {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q, currently %s; start/repair the dependency first", res.ID, dep, state))
		}
	}
	return warnings
}

// dependencyCycles is the cycle half of the old build's resourceStartupOrder from root.
func dependencyCycles(resources []config.ResourceDef, root string) []string {
	byID := make(map[string]config.ResourceDef, len(resources))
	for _, r := range resources {
		byID[r.ID] = r
	}
	state := map[string]int{}
	var warnings, stack []string
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			warnings = append(warnings, "dependency cycle: "+strings.Join(append(append([]string(nil), stack...), id), " -> "))
			return
		}
		r, ok := byID[id]
		if !ok {
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dep := range r.DependsOn {
			visit(dep)
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	visit(root)
	return warnings
}

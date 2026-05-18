package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/chrispian/cerberus/internal/registry"
)

// ResourceRuntimeService owns resource-native local runtime operations.
// CLI fallback, socket handlers, MCP, and any future GUI-facing API should
// route through this layer rather than reimplementing resource logic.
type ResourceRuntimeService struct {
	local  *localconn.Connector
	logger *slog.Logger

	cfgPath string

	cfgMu sync.RWMutex
	cfg   *config.ConfigV2

	// drift holds the optional background artifact-drift cache. When set
	// (daemon mode), ListResources reads repo-drift staleness from it
	// instead of running a live git probe per poll.
	drift atomic.Pointer[DriftCache]

	opMu sync.Mutex
}

// AttachDriftCache wires a background drift cache into the runtime so the
// list path can surface repo-drift staleness. Call once during daemon
// startup, before the runtime begins serving requests.
func (s *ResourceRuntimeService) AttachDriftCache(d *DriftCache) {
	s.drift.Store(d)
}

// lookupDrift returns cached artifact drift for a resource when a drift
// cache is attached and holds a fresh entry.
func (s *ResourceRuntimeService) lookupDrift(id string) (localconn.ArtifactStatus, bool) {
	d := s.drift.Load()
	if d == nil {
		return localconn.ArtifactStatus{}, false
	}
	return d.Lookup(id)
}

const resourceStatusProbeTimeout = 750 * time.Millisecond

type ResourceRuntimeOption func(*ResourceRuntimeService)

func WithResourceRuntimeConfigV2(cfg *config.ConfigV2) ResourceRuntimeOption {
	return func(s *ResourceRuntimeService) { s.cfg = cfg }
}

func WithResourceRuntimeConfigPath(path string) ResourceRuntimeOption {
	return func(s *ResourceRuntimeService) { s.cfgPath = path }
}

func WithResourceRuntimeLocalConnector(local *localconn.Connector) ResourceRuntimeOption {
	return func(s *ResourceRuntimeService) { s.local = local }
}

func WithResourceRuntimeLogger(logger *slog.Logger) ResourceRuntimeOption {
	return func(s *ResourceRuntimeService) {
		if logger != nil {
			s.logger = logger
		}
	}
}

func NewResourceRuntimeService(opts ...ResourceRuntimeOption) *ResourceRuntimeService {
	s := &ResourceRuntimeService{logger: slog.Default()}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *ResourceRuntimeService) ListResources(ctx context.Context, args ResourceListArgs) ([]ResourceInfo, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}

	type resourceRow struct {
		info ResourceInfo
		res  config.ResourceDef
		spec localconn.ProcessSpec
	}

	var rows []resourceRow
	for _, r := range cfg.Resources {
		if args.ProjectID != "" && r.Project != args.ProjectID {
			continue
		}
		if args.Connector != "" && r.Connector != args.Connector {
			continue
		}
		if args.Tag != "" && !containsTagFold(r.Tags, args.Tag) {
			continue
		}
		row := resourceRow{res: r}
		info := ResourceInfo{
			ID:         r.ID,
			Name:       r.Name,
			Type:       r.Type,
			Project:    r.Project,
			Connector:  r.Connector,
			Mode:       resourceMode(r),
			Supervisor: resourceSupervisor(r),
			RunFrom:    resourceRunFrom(r),
			URL:        configString(r.Config, "url"),
			Port:       configInt(r.Config, "port"),
			HasBuild:   len(configStringSlice(r.Config, "build")) > 0,
			Tags:       append([]string(nil), r.Tags...),
		}
		if r.Type == string(domain.ResourceProcess) && r.Connector == "local" {
			row.spec, _ = localconn.SpecFromResourceConfig(r.Config)
			info.OperatorStopped = pausectl.IsServicePaused(r.ID)
		}
		row.info = info
		rows = append(rows, row)
	}

	out := make([]ResourceInfo, len(rows))
	var wg sync.WaitGroup
	for i := range rows {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := rows[i].info
			res := rows[i].res
			spec := rows[i].spec
			if res.Type == string(domain.ResourceProcess) && res.Connector == "local" {
				dr := resourceDefToDomain(&res)
				if state, err := s.statusWithTimeout(ctx, dr); err == nil {
					info.Status = string(state)
				}
				// Prefer the background drift cache: it runs the full
				// repo-aware probe so the table can surface repo-drift
				// staleness. Fall back to the basic probe before the
				// first scan completes (or when no cache is attached).
				art, haveArt := s.lookupDrift(res.ID)
				if !haveArt {
					if _, basic, err := localconn.InspectArtifactInstallBasic(dr, spec); err == nil {
						art, haveArt = basic, true
					}
				}
				if haveArt {
					info.ArtifactInstalled = art.Installed
					info.ArtifactStale = art.Stale
					if info.Status != "" {
						if action, reason := localconn.RecommendedStatusAction(spec, domain.State(info.Status), art); action != "" {
							info.RecommendedAction = action
							info.RecommendedNextStep = localconn.RecommendedNextStep(action, reason)
						}
					}
				}
			}
			out[i] = info
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *ResourceRuntimeService) GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, fmt.Errorf("no config available")
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return nil, fmt.Errorf("resource %q is %s/%s; runtime status currently supports local process resources only", res.ID, res.Type, res.Connector)
	}

	dr := resourceDefToDomain(res)
	state, err := s.localConnector().Status(ctx, dr)
	if err != nil {
		return nil, err
	}

	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	installRoot := spec.InstallRoot
	serviceName := spec.ServiceName
	artifactPath := spec.ArtifactPath
	artifactInstalled := false
	artifactStale := false
	artifactStaleReason := ""
	artifactSource := ""
	artifactSyncedAt := ""
	recommendedAction := ""
	recommendedReason := ""
	var launchdRec localconn.LaunchdRecord
	if spec.Mode == localconn.ProcessModeOSService {
		if layout, art, inspectErr := localconn.InspectArtifactInstall(dr, spec); inspectErr == nil {
			if installRoot == "" {
				installRoot = layout.RootDir
			}
			if serviceName == "" {
				serviceName = layout.ServiceName
			}
			if artifactPath == "" {
				artifactPath = layout.ArtifactPath
			}
			artifactInstalled = art.Installed
			artifactStale = art.Stale
			artifactStaleReason = art.StaleReason
			artifactSource = art.SourcePath
			if !art.SyncedAt.IsZero() {
				artifactSyncedAt = art.SyncedAt.Format(time.RFC3339)
			}
			recommendedAction, recommendedReason = localconn.RecommendedStatusAction(spec, state, art)
		}
		if rec, recErr := localconn.InspectLaunchdRecord(ctx, dr, spec); recErr == nil {
			launchdRec = rec
		}
	}

	return &ResourceRuntimeStatus{
		ID:                  res.ID,
		Name:                res.Name,
		Type:                res.Type,
		Project:             res.Project,
		Connector:           res.Connector,
		Mode:                resourceMode(*res),
		Supervisor:          resourceSupervisor(*res),
		RunFrom:             resourceRunFrom(*res),
		URL:                 spec.URL,
		Port:                spec.Port,
		HasBuild:            len(spec.Build) > 0,
		Status:              string(state),
		OperatorStopped:     pausectl.IsServicePaused(res.ID),
		ServiceName:         serviceName,
		ArtifactPath:        artifactPath,
		InstallRoot:         installRoot,
		ArtifactInstalled:   artifactInstalled,
		ArtifactStale:       artifactStale,
		ArtifactStaleReason: artifactStaleReason,
		ArtifactSource:      artifactSource,
		ArtifactSyncedAt:    artifactSyncedAt,
		RecommendedAction:   recommendedAction,
		RecommendedReason:   recommendedReason,
		RecommendedNextStep: localconn.RecommendedNextStep(recommendedAction, recommendedReason),
		LaunchdLoaded:       launchdRec.Loaded,
		LaunchdState:        launchdRec.State,
		LaunchdPID:          launchdRec.PID,
		LaunchdLastExitCode: launchdRec.LastExitCode,
		LaunchdThrottled:    launchdRec.Throttled,
		LaunchdReason:       launchdRec.Reason,
		LaunchdDiagnosis:    launchdRec.Diagnosis,
		LaunchdHighlights:   append([]string(nil), launchdRec.Highlights...),
	}, nil
}

func (s *ResourceRuntimeService) GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, fmt.Errorf("no config available")
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return nil, fmt.Errorf("resource %q is %s/%s; inspect currently supports local process resources only", res.ID, res.Type, res.Connector)
	}

	dr := resourceDefToDomain(res)
	state, err := s.localConnector().Status(ctx, dr)
	if err != nil {
		return nil, err
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)

	out := &ResourceInspect{
		ID:              res.ID,
		Name:            res.Name,
		Type:            res.Type,
		Project:         res.Project,
		Connector:       res.Connector,
		Mode:            resourceMode(*res),
		Supervisor:      resourceSupervisor(*res),
		RunFrom:         resourceRunFrom(*res),
		URL:             spec.URL,
		Port:            spec.Port,
		Status:          string(state),
		OperatorStopped: pausectl.IsServicePaused(res.ID),
		WorkspaceDir:    spec.Dir,
		Command:         append([]string(nil), spec.Command...),
		Build:           append([]string(nil), spec.Build...),
		WorkingDir:      spec.Dir,
	}

	if spec.Mode == localconn.ProcessModeOSService {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			if layout, layoutErr := localconn.DefaultInstallLayout(home, dr, spec); layoutErr == nil {
				out.WorkingDir = layout.WorkingDir
				out.ServiceName = layout.ServiceName
				out.PlistPath = layout.PlistPath
				out.InstallRoot = layout.RootDir
				out.InstallWorkDir = layout.CurrentDir
				out.BinDir = layout.BinDir
				out.ArtifactPath = layout.ArtifactPath
				out.StdoutLogPath = filepath.Join(layout.RootDir, "logs", "stdout.log")
				out.StderrLogPath = filepath.Join(layout.RootDir, "logs", "stderr.log")
			}
		}
		if layout, art, inspectErr := localconn.InspectArtifactInstall(dr, spec); inspectErr == nil {
			if out.ServiceName == "" {
				out.ServiceName = layout.ServiceName
			}
			if out.PlistPath == "" {
				out.PlistPath = layout.PlistPath
			}
			if out.InstallRoot == "" {
				out.InstallRoot = layout.RootDir
			}
			if out.InstallWorkDir == "" {
				out.InstallWorkDir = layout.CurrentDir
			}
			if out.BinDir == "" {
				out.BinDir = layout.BinDir
			}
			if out.ArtifactPath == "" {
				out.ArtifactPath = layout.ArtifactPath
			}
			if out.StdoutLogPath == "" {
				out.StdoutLogPath = filepath.Join(layout.RootDir, "logs", "stdout.log")
			}
			if out.StderrLogPath == "" {
				out.StderrLogPath = filepath.Join(layout.RootDir, "logs", "stderr.log")
			}
			out.ArtifactInstalled = art.Installed
			out.ArtifactStale = art.Stale
			out.ArtifactStaleReason = art.StaleReason
			out.ArtifactSource = art.SourcePath
			if !art.SyncedAt.IsZero() {
				out.ArtifactSyncedAt = art.SyncedAt.Format(time.RFC3339)
			}
			out.RecommendedAction, out.RecommendedReason = localconn.RecommendedStatusAction(spec, state, art)
			out.RecommendedNextStep = localconn.RecommendedNextStep(out.RecommendedAction, out.RecommendedReason)
		}
		if rec, recErr := localconn.InspectLaunchdRecord(ctx, dr, spec); recErr == nil {
			out.LaunchdLoaded = rec.Loaded
			out.LaunchdState = rec.State
			out.LaunchdPID = rec.PID
			out.LaunchdLastExitCode = rec.LastExitCode
			out.LaunchdThrottled = rec.Throttled
			out.LaunchdReason = rec.Reason
			out.LaunchdDiagnosis = rec.Diagnosis
			out.LaunchdHighlights = append([]string(nil), rec.Highlights...)
			out.LaunchdRaw = rec.Raw
		}
	}

	return out, nil
}

func (s *ResourceRuntimeService) GetResourceDoctor(ctx context.Context, id string) (*ResourceDoctor, error) {
	inspect, err := s.GetResourceInspect(ctx, id)
	if err != nil {
		return nil, err
	}

	checks := make([]ResourceDoctorCheck, 0, 8)
	add := func(name, status, msg string) {
		checks = append(checks, ResourceDoctorCheck{Name: name, Status: status, Message: msg})
	}

	if inspect.Status == "" || inspect.Status == string(domain.StateUnknown) {
		add("runtime_status", "warn", "runtime state is unknown")
	} else {
		add("runtime_status", "pass", fmt.Sprintf("runtime state is %s", inspect.Status))
	}
	if inspect.OperatorStopped {
		add("operator_stop", "pass", "operator stop is active; apply, deploy, or reload will resume auto-restart for dev_session resources")
	}

	if inspect.Mode == string(localconn.ProcessModeOSService) {
		checkPathCheck := func(name, path string, required bool) {
			if path == "" {
				if required {
					add(name, "fail", "path is not configured")
				} else {
					add(name, "warn", "path is not configured")
				}
				return
			}
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					if required {
						add(name, "fail", fmt.Sprintf("path does not exist: %s", path))
					} else {
						add(name, "warn", fmt.Sprintf("path does not exist: %s", path))
					}
					return
				}
				add(name, "warn", fmt.Sprintf("could not stat path %s: %v", path, err))
				return
			}
			add(name, "pass", path)
		}

		checkPathCheck("install_root", inspect.InstallRoot, true)
		checkPathCheck("plist", inspect.PlistPath, true)
		checkPathCheck("stdout_log", inspect.StdoutLogPath, false)
		checkPathCheck("stderr_log", inspect.StderrLogPath, false)

		if inspect.RunFrom == string(localconn.ProcessRunFromArtifact) {
			switch {
			case !inspect.ArtifactInstalled:
				add("artifact_install", "fail", "installed artifact is missing")
			case inspect.ArtifactStale:
				add("artifact_install", "warn", fmt.Sprintf("installed artifact is stale (%s)", inspect.ArtifactStaleReason))
			default:
				add("artifact_install", "pass", "installed artifact is current")
			}

			if inspect.ArtifactSource != "" {
				if _, err := os.Stat(inspect.ArtifactSource); err != nil {
					if os.IsNotExist(err) {
						add("artifact_source", "fail", fmt.Sprintf("source artifact is missing: %s", inspect.ArtifactSource))
					} else {
						add("artifact_source", "warn", fmt.Sprintf("could not stat source artifact %s: %v", inspect.ArtifactSource, err))
					}
				} else {
					add("artifact_source", "pass", inspect.ArtifactSource)
				}
			}
		} else if inspect.RunFrom == string(localconn.ProcessRunFromWorkspace) {
			if len(inspect.Command) > 0 {
				add("workspace_runtime", "pass", "resource runs directly from the workspace")
			} else {
				add("workspace_runtime", "warn", "workspace-backed resource has no command configured")
			}
		}
		if inspect.LaunchdLoaded {
			add("launchd_loaded", "pass", fmt.Sprintf("launchd state is %s", valueOrUnknown(inspect.LaunchdState)))
		} else {
			add("launchd_loaded", "warn", "launchd service is not loaded")
		}
		if inspect.LaunchdThrottled {
			add("launchd_throttle", "fail", "launchd reports the service as throttled")
		}
		if inspect.LaunchdLastExitCode != nil {
			if *inspect.LaunchdLastExitCode == 0 {
				add("launchd_exit", "pass", "last exit code is 0")
			} else {
				add("launchd_exit", "warn", fmt.Sprintf("last exit code is %d", *inspect.LaunchdLastExitCode))
			}
		}
		if inspect.LaunchdReason != "" {
			add("launchd_reason", "warn", inspect.LaunchdReason)
		}
		if inspect.LaunchdDiagnosis != "" {
			status := "warn"
			diagnosis := strings.TrimSpace(strings.ToLower(inspect.LaunchdDiagnosis))
			switch {
			case inspect.LaunchdThrottled:
				status = "fail"
			case diagnosis == "service is loaded and running":
				status = "pass"
			case inspect.LaunchdLoaded && strings.TrimSpace(strings.ToLower(inspect.LaunchdState)) == "running":
				status = "pass"
			}
			add("launchd_diagnosis", status, inspect.LaunchdDiagnosis)
		}
	}

	failCount := 0
	warnCount := 0
	for _, c := range checks {
		switch c.Status {
		case "fail":
			failCount++
		case "warn":
			warnCount++
		}
	}

	summary := "all checks passed"
	switch {
	case failCount > 0:
		summary = fmt.Sprintf("%d failed, %d warning", failCount, warnCount)
		if warnCount != 1 {
			summary = fmt.Sprintf("%d failed, %d warnings", failCount, warnCount)
		}
	case warnCount > 0:
		if warnCount == 1 {
			summary = "1 warning"
		} else {
			summary = fmt.Sprintf("%d warnings", warnCount)
		}
	}

	return &ResourceDoctor{
		ResourceID:          inspect.ID,
		Status:              inspect.Status,
		OperatorStopped:     inspect.OperatorStopped,
		Summary:             summary,
		RecommendedAction:   inspect.RecommendedAction,
		RecommendedReason:   inspect.RecommendedReason,
		RecommendedNextStep: inspect.RecommendedNextStep,
		Checks:              checks,
	}, nil
}

func (s *ResourceRuntimeService) ReloadResource(ctx context.Context, id string) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	if spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession {
		_ = pausectl.ResumeService(id)
	}
	if err := s.localConnector().Reload(ctx, resourceDefToDomain(res)); err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("resource %q reloaded successfully", id),
	}, nil
}

func (s *ResourceRuntimeService) StopResource(ctx context.Context, id string) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	paused := false
	if spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession {
		if err := pausectl.PauseService(id); err != nil {
			return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
		}
		paused = true
	}
	if err := s.localConnector().Stop(ctx, resourceDefToDomain(res)); err != nil {
		if paused {
			_ = pausectl.ResumeService(id)
		}
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("resource %q stopped successfully; install state preserved", id),
	}, nil
}

func (s *ResourceRuntimeService) DeployResource(ctx context.Context, id string, options ...DeployResourceOption) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	opts := ApplyDeployResourceOptions(options)

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	installAfterBuild := s.resolveInstallAfterBuild(res.Config, spec, opts)
	buildOutput := ""
	installOutput := ""
	installSkipped := false
	if len(spec.Build) > 0 {
		out, buildErr := localconn.BuildProcess(spec)
		buildOutput = strings.TrimSpace(out)
		if buildErr != nil {
			return &OpResult{
				Success:     false,
				ServiceID:   id,
				BuildOutput: buildOutput,
				Error:       fmt.Sprintf("build failed for resource %q: %s", id, buildErr.Error()),
			}, nil
		}
		if installAfterBuild {
			skipped, instOut, installErr := localconn.RunInstall(spec)
			installOutput = strings.TrimSpace(instOut)
			installSkipped = skipped
			if installErr != nil {
				return &OpResult{
					Success:        false,
					ServiceID:      id,
					BuildOutput:    buildOutput,
					InstallOutput:  installOutput,
					InstallSkipped: false,
					Error:          fmt.Sprintf("install failed for resource %q: %s", id, installErr.Error()),
				}, nil
			}
			if skipped {
				s.logger.Info("resource_runtime.install.skipped",
					"resource", id,
					"reason", "no_install_target_in_makefile",
				)
			}
		}
	}
	if spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession {
		_ = pausectl.ResumeService(id)
	}
	applyRes, applyErr := s.localConnector().Apply(ctx, resourceDefToDomain(res))
	if applyErr != nil {
		return &OpResult{
			Success:        false,
			ServiceID:      id,
			BuildOutput:    buildOutput,
			InstallOutput:  installOutput,
			InstallSkipped: installSkipped,
			Error:          s.formatApplyError(id, res, spec, applyErr),
		}, nil
	}
	msg := localconn.FormatApplyResultMessage(id, spec, applyRes)
	if len(spec.Build) > 0 {
		msg = fmt.Sprintf("resource %q deployed successfully (%s)", id, strings.TrimPrefix(msg, fmt.Sprintf("resource %q ", id)))
	}
	return &OpResult{
		Success:        true,
		ServiceID:      id,
		BuildOutput:    buildOutput,
		InstallOutput:  installOutput,
		InstallSkipped: installSkipped,
		Message:        msg,
	}, nil
}

// resolveInstallAfterBuild is the method-shaped entry point used by
// DeployResource. It snapshots the live config and delegates to the pure
// resolver so the precedence logic stays testable in isolation.
func (s *ResourceRuntimeService) resolveInstallAfterBuild(rawCfg map[string]any, spec localconn.ProcessSpec, opts DeployResourceOpts) bool {
	return ResolveInstallAfterBuild(rawCfg, spec.InstallAfterBuild, s.snapshotConfig().InstallAfterBuildDefault(), opts.InstallAfterBuildOverride)
}

// ResolveInstallAfterBuild collapses the three-layer precedence (CLI override >
// resource-level > global default) into the bool that BuildProcess + RunInstall
// act on. Resource-level presence is detected via the raw config map, not the
// parsed spec, because plain-bool ProcessSpec.InstallAfterBuild can't
// distinguish "absent" from "explicit false" on its own.
//
// Inputs:
//   - rawCfg: the resource's raw config map (presence check key).
//   - resourceVal: the parsed spec.InstallAfterBuild value (used only when
//     the raw map has the key).
//   - globalDefault: the layer-3 default from ConfigV2.InstallAfterBuildDefault().
//   - override: layer-1 CLI override; nil = no override.
func ResolveInstallAfterBuild(rawCfg map[string]any, resourceVal bool, globalDefault bool, override *bool) bool {
	if override != nil {
		return *override
	}
	if _, present := rawCfg["install_after_build"]; present {
		return resourceVal
	}
	return globalDefault
}

func (s *ResourceRuntimeService) ApplyResource(ctx context.Context, id string) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	if spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession {
		_ = pausectl.ResumeService(id)
	}
	applyRes, applyErr := s.localConnector().Apply(ctx, resourceDefToDomain(res))
	if applyErr != nil {
		return &OpResult{Success: false, ServiceID: id, Error: s.formatApplyError(id, res, spec, applyErr)}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   localconn.FormatApplyResultMessage(id, spec, applyRes),
	}, nil
}

func (s *ResourceRuntimeService) formatApplyError(id string, res *config.ResourceDef, spec localconn.ProcessSpec, err error) string {
	msg := strings.TrimSpace(err.Error())
	inspectHint := fmt.Sprintf("run `cerberus resource doctor %s` for a full runtime/install check", id)
	if spec.Mode != localconn.ProcessModeOSService {
		return fmt.Sprintf("%s; %s", msg, inspectHint)
	}
	dr := resourceDefToDomain(res)
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if layout, layoutErr := localconn.DefaultInstallLayout(home, dr, spec); layoutErr == nil {
			return fmt.Sprintf("%s; stderr log: %s; stdout log: %s; plist: %s; install: %s; artifact: %s; %s",
				msg,
				filepath.Join(layout.RootDir, "logs", "stderr.log"),
				filepath.Join(layout.RootDir, "logs", "stdout.log"),
				layout.PlistPath,
				layout.RootDir,
				layout.ArtifactPath,
				inspectHint,
			)
		}
	}
	return fmt.Sprintf("%s; %s", msg, inspectHint)
}

func (s *ResourceRuntimeService) SyncResource(_ context.Context, id string) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	if spec.RunFrom != localconn.ProcessRunFromArtifact {
		return &OpResult{
			Success:   true,
			ServiceID: id,
			Message:   fmt.Sprintf("resource %q does not use artifact mode; nothing to sync", id),
		}, nil
	}
	_, syncRes, syncErr := localconn.SyncArtifactInstall(resourceDefToDomain(res), spec)
	if syncErr != nil {
		return &OpResult{Success: false, ServiceID: id, Error: syncErr.Error()}, nil
	}
	msg := fmt.Sprintf("resource %q artifact already current", id)
	if syncRes.Changed {
		msg = fmt.Sprintf("resource %q artifact synced", id)
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   msg,
	}, nil
}

func (s *ResourceRuntimeService) RemoveResource(ctx context.Context, id string) (*OpResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	if spec.Mode == "" || spec.Mode == localconn.ProcessModeDevSession {
		_ = pausectl.PauseService(id)
	}
	if err := s.localConnector().Destroy(ctx, resourceDefToDomain(res)); err != nil {
		return &OpResult{Success: false, ServiceID: id, Error: err.Error()}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("resource %q removed successfully", id),
	}, nil
}

func (s *ResourceRuntimeService) ResourceLogs(_ context.Context, id string, lines int, stream string) (*LogLines, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	res, spec, err := s.requireLocalProcessSpec(id)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = 50
	}
	logPath, err := s.resourceLogPath(resourceDefToDomain(res), spec, stream)
	if err != nil {
		return nil, err
	}
	content, err := readLastNLines(logPath, lines)
	if err != nil {
		if os.IsNotExist(err) {
			return &LogLines{
				ResourceID: id,
				Stream:     stream,
				LogPath:    logPath,
				Content:    fmt.Sprintf("Log file does not exist: %s", logPath),
			}, nil
		}
		return nil, err
	}
	return &LogLines{
		ResourceID: id,
		Stream:     stream,
		LogPath:    logPath,
		Content:    content,
	}, nil
}

func (s *ResourceRuntimeService) Health(ctx context.Context, id string) ([]ResourceHealth, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}

	var selected []config.ResourceDef
	for _, r := range cfg.Resources {
		if id != "" && r.ID != id {
			continue
		}
		if r.Type != string(domain.ResourceProcess) || r.Connector != "local" {
			continue
		}
		selected = append(selected, r)
	}

	out := make([]ResourceHealth, len(selected))
	var wg sync.WaitGroup
	for i := range selected {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := selected[i]
			spec, err := localconn.SpecFromResourceConfig(r.Config)
			if err != nil {
				out[i] = ResourceHealth{
					ResourceID:        r.ID,
					Status:            string(domain.StateUnknown),
					Healthy:           false,
					Mode:              resourceMode(r),
					Supervisor:        resourceSupervisor(r),
					RunFrom:           resourceRunFrom(r),
					RecommendedReason: fmt.Sprintf("decode process spec: %v", err),
				}
				return
			}
			dr := resourceDefToDomain(&r)
			state, err := s.statusWithTimeout(ctx, dr)
			if err != nil {
				out[i] = ResourceHealth{
					ResourceID:        r.ID,
					Status:            string(domain.StateUnknown),
					Healthy:           false,
					Mode:              resourceMode(r),
					Supervisor:        resourceSupervisor(r),
					RunFrom:           resourceRunFrom(r),
					RecommendedReason: err.Error(),
				}
				return
			}
			health := ResourceHealth{
				ResourceID:      r.ID,
				Status:          string(state),
				Healthy:         resourceStateHealthy(state),
				OperatorStopped: pausectl.IsServicePaused(r.ID),
				Mode:            resourceMode(r),
				Supervisor:      resourceSupervisor(r),
				RunFrom:         resourceRunFrom(r),
			}
			if _, art, inspectErr := localconn.InspectArtifactInstallBasic(dr, spec); inspectErr == nil {
				health.ArtifactInstalled = art.Installed
				health.ArtifactStale = art.Stale
				health.RecommendedAction, health.RecommendedReason = localconn.RecommendedStatusAction(spec, state, art)
				health.RecommendedNextStep = localconn.RecommendedNextStep(health.RecommendedAction, health.RecommendedReason)
				if health.Healthy && art.Stale && health.RecommendedAction != "" {
					health.Healthy = false
				}
			}
			out[i] = health
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *ResourceRuntimeService) statusWithTimeout(ctx context.Context, dr *domain.Resource) (domain.State, error) {
	probeCtx, cancel := context.WithTimeout(ctx, resourceStatusProbeTimeout)
	defer cancel()

	state, err := s.localConnector().Status(probeCtx, dr)
	if err == nil {
		return state, nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return domain.StateUnknown, fmt.Errorf("status probe timed out after %s", resourceStatusProbeTimeout)
	}
	return domain.StateUnknown, err
}

func (s *ResourceRuntimeService) requireLocalProcessResource(id string) (*config.ResourceDef, error) {
	cfg := s.snapshotConfig()
	if cfg == nil {
		return nil, fmt.Errorf("no config available")
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return nil, fmt.Errorf("resource %q is %s/%s; operation currently supports local process resources only", res.ID, res.Type, res.Connector)
	}
	return res, nil
}

func (s *ResourceRuntimeService) requireLocalProcessSpec(id string) (*config.ResourceDef, localconn.ProcessSpec, error) {
	res, err := s.requireLocalProcessResource(id)
	if err != nil {
		return nil, localconn.ProcessSpec{}, err
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return nil, localconn.ProcessSpec{}, fmt.Errorf("decode process spec for %q: %w", id, err)
	}
	return res, spec, nil
}

func (s *ResourceRuntimeService) snapshotConfig() *config.ConfigV2 {
	if s.cfgPath == "" {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		return s.cfg
	}
	fresh, err := registry.ResolveConfig(s.cfgPath)
	if err != nil {
		s.logger.Warn("resource_runtime.config_reload.failed",
			"path", s.cfgPath,
			"error", err.Error(),
		)
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		return s.cfg
	}
	s.cfgMu.Lock()
	s.cfg = fresh
	s.cfgMu.Unlock()
	return fresh
}

func (s *ResourceRuntimeService) localConnector() *localconn.Connector {
	if s.local != nil {
		return s.local
	}
	return localconn.New()
}

func (s *ResourceRuntimeService) resourceLogPath(res *domain.Resource, spec localconn.ProcessSpec, stream string) (string, error) {
	switch spec.Mode {
	case "", localconn.ProcessModeDevSession:
		return localconn.DevSessionLogPath(res.ID, spec), nil
	case localconn.ProcessModeOSService:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		layout, err := localconn.DefaultInstallLayout(home, res, spec)
		if err != nil {
			return "", err
		}
		logName := "stdout.log"
		if strings.EqualFold(stream, "stderr") {
			logName = "stderr.log"
		}
		return filepath.Join(layout.RootDir, "logs", logName), nil
	default:
		return "", fmt.Errorf("unsupported process mode %q for resource %q", spec.Mode, res.ID)
	}
}

func containsTagFold(tags []string, target string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, target) {
			return true
		}
	}
	return false
}

func findResourceDef(cfg *config.ConfigV2, id string) *config.ResourceDef {
	if cfg == nil {
		return nil
	}
	for i := range cfg.Resources {
		if cfg.Resources[i].ID == id {
			return &cfg.Resources[i]
		}
	}
	return nil
}

func resourceDefToDomain(r *config.ResourceDef) *domain.Resource {
	return &domain.Resource{
		ID:        r.ID,
		Name:      r.Name,
		Type:      domain.ResourceType(r.Type),
		ProjectID: r.Project,
		Connector: r.Connector,
		Config:    r.Config,
		Tags:      append([]string(nil), r.Tags...),
		DependsOn: append([]string(nil), r.DependsOn...),
	}
}

func resourceMode(r config.ResourceDef) string {
	if r.Type != string(domain.ResourceProcess) || r.Connector != "local" {
		return ""
	}
	spec, err := localconn.SpecFromResourceConfig(r.Config)
	if err != nil {
		return ""
	}
	if spec.Mode == "" {
		return string(localconn.ProcessModeDevSession)
	}
	return string(spec.Mode)
}

func resourceSupervisor(r config.ResourceDef) string {
	if r.Type != string(domain.ResourceProcess) || r.Connector != "local" {
		return ""
	}
	spec, err := localconn.SpecFromResourceConfig(r.Config)
	if err != nil {
		return ""
	}
	if spec.Supervisor == "" {
		return string(localconn.ProcessSupervisorAuto)
	}
	return string(spec.Supervisor)
}

func resourceRunFrom(r config.ResourceDef) string {
	if r.Type != string(domain.ResourceProcess) || r.Connector != "local" {
		return ""
	}
	spec, err := localconn.SpecFromResourceConfig(r.Config)
	if err != nil {
		return ""
	}
	if spec.RunFrom == "" {
		return string(localconn.ProcessRunFromWorkspace)
	}
	return string(spec.RunFrom)
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

func configInt(cfg map[string]any, key string) int {
	if cfg == nil {
		return 0
	}
	switch v := cfg[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func configStringSlice(cfg map[string]any, key string) []string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func resourceStateHealthy(state domain.State) bool {
	switch state {
	case domain.StateRunning, domain.StateHealthy:
		return true
	default:
		return false
	}
}

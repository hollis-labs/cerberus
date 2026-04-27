package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/chrispian/cerberus/internal/pipeline"
	"github.com/chrispian/cerberus/internal/procscan"
	"github.com/chrispian/cerberus/internal/service"
)

// InProcessClient satisfies Client by driving a live ServiceRegistry
// directly. Used by the daemon process itself (for daemon-embedded MCP
// handlers) and by the socket server as its backend.
//
// The client is a thin adapter — all Reload() semantics from CERB-1 are
// preserved by routing through reg.Reload() at the top of each
// lifecycle op, matching the pattern established in internal/mcp.
type InProcessClient struct {
	reg     *service.ServiceRegistry
	monitor *daemon.Monitor
	local   *localconn.Connector
	logger  *slog.Logger

	// cfgPath is the on-disk path for the unified v2 config. When set,
	// every project / resource / pipeline endpoint re-reads the file
	// via config.LoadUnified(cfgPath) before serving — this is the
	// analog of ServiceRegistry.Reload() for the v2 config tree and
	// closes the staleness bug that CERB-2 exists to prevent.
	//
	// Empty in tests that seed cfg in-memory via WithConfigV2.
	cfgPath string

	// cfgMu guards cfg. cfg holds the last-known-good ConfigV2
	// snapshot: it is hydrated from LoadUnified(cfgPath) on each
	// project/resource/pipeline call (when cfgPath is set) and used
	// as a read-only fallback when reload fails.
	cfgMu sync.RWMutex
	cfg   *config.ConfigV2

	// opMu serializes registry reloads, service polling, and the
	// lifecycle critical sections executed on this client's behalf.
	//
	// ManagedService.Poll / ServiceRegistry.Reload mutate service
	// state without fine-grained locking at the ManagedService level
	// (pre-existing concern in internal/service — a single daemon
	// process historically only hit these paths serially). Once N
	// MCP subprocesses fire concurrent RPCs through the socket, every
	// inbound request triggers Reload + Poll — so without this mutex
	// the race detector (correctly) flags writes to *ManagedService
	// fields. Fixing that at the service-package level is out of
	// scope for CERB-2; serializing at the Client layer is a
	// targeted, correct workaround.
	opMu sync.Mutex
}

// InProcessOption tunes construction of an InProcessClient.
type InProcessOption func(*InProcessClient)

// WithMonitor attaches a daemon monitor so Health/Status can surface
// restart stats. Optional — callers without a monitor pass nothing.
func WithMonitor(m *daemon.Monitor) InProcessOption {
	return func(c *InProcessClient) { c.monitor = m }
}

// WithConfigV2 seeds an in-memory v2 config snapshot. Intended for
// tests that don't want a file-backed config. Production callers
// should use WithConfigPath so the client re-reads the file on every
// project/resource/pipeline call (eliminating staleness).
//
// When both WithConfigV2 and WithConfigPath are set, WithConfigV2
// becomes the initial last-good snapshot used only as a fallback when
// the on-disk load fails.
func WithConfigV2(cfg *config.ConfigV2) InProcessOption {
	return func(c *InProcessClient) { c.cfg = cfg }
}

// WithConfigPath stashes the on-disk path for the unified v2 config.
// When set, ListProjects / ListResources / ListPipelines / RunPipeline
// call config.LoadUnified(path) on every invocation so edits to
// config.yaml surface without a daemon restart — the v2-config analog
// of ServiceRegistry.Reload() for the service tree.
//
// Production wiring: the daemon passes the same cfgPath that app.New
// used to bootstrap its initial Config.
func WithConfigPath(path string) InProcessOption {
	return func(c *InProcessClient) { c.cfgPath = path }
}

// WithLocalConnector attaches the local connector used by pipeline
// execution. Required for RunPipeline; unused otherwise.
func WithLocalConnector(l *localconn.Connector) InProcessOption {
	return func(c *InProcessClient) { c.local = l }
}

// WithInProcessLogger attaches a slog logger for InProcessClient
// events such as config/registry reload failures. Defaults to
// slog.Default() when not set.
//
// Named explicitly (rather than WithLogger / WithClientLogger) to
// avoid package-level collision with SocketServer.WithLogger and
// SocketClient.WithClientLogger.
func WithInProcessLogger(l *slog.Logger) InProcessOption {
	return func(c *InProcessClient) {
		if l != nil {
			c.logger = l
		}
	}
}

// NewInProcessClient constructs an InProcessClient. reg is required; all
// other collaborators are optional (see WithMonitor, WithConfigV2,
// WithConfigPath, WithLocalConnector, WithInProcessLogger).
func NewInProcessClient(reg *service.ServiceRegistry, opts ...InProcessOption) *InProcessClient {
	c := &InProcessClient{reg: reg, logger: slog.Default()}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// reloadLogged reloads the registry from disk, logging any failure.
// The fallback behavior (continue with last-good registry state) is
// intentional — a corrupt config file should not take the daemon
// offline. But silent failure is the "looks fine but isn't" class of
// bug CERB-2 exists to eliminate, so we always surface the error on
// the logger.
func (c *InProcessClient) reloadLogged() {
	if err := c.reg.Reload(); err != nil {
		c.logger.Warn("client.reload.failed", "error", err.Error())
	}
}

// reloadAndFind refreshes the registry from disk before resolving. This
// mirrors internal/mcp.reloadAndFind — the sole purpose of this method
// is to ensure every lifecycle op sees the current on-disk config.
//
// Reload itself is protected by ServiceRegistry.mu, but the registry
// mutates existing *ManagedService pointers in-place during Reload
// (Def, Stale) so readers must not hold those pointers across Reload
// calls from other goroutines. Holding opMu keeps callers in a safe
// quiescent window.
func (c *InProcessClient) reloadAndFind(id string) *service.ManagedService {
	c.reloadLogged()
	return c.reg.Find(id)
}

// snapshotConfig returns the current v2 config snapshot used by the
// project/resource/pipeline endpoints. When cfgPath is set it re-reads
// the file on every call (closing the staleness bug for v2 config).
// On read failure it falls back to the last-good snapshot — and logs
// the error so silent drift can't hide.
//
// When cfgPath is not set (tests seeding cfg via WithConfigV2), the
// stored pointer is returned directly.
//
// The returned *ConfigV2 is safe to read for the duration of a single
// call: the client never mutates it in place — reload replaces the
// whole pointer under cfgMu.
func (c *InProcessClient) snapshotConfig() *config.ConfigV2 {
	if c.cfgPath == "" {
		c.cfgMu.RLock()
		defer c.cfgMu.RUnlock()
		return c.cfg
	}
	fresh, err := config.LoadUnified(c.cfgPath)
	if err != nil {
		c.logger.Warn("client.config_reload.failed",
			"path", c.cfgPath,
			"error", err.Error(),
		)
		c.cfgMu.RLock()
		defer c.cfgMu.RUnlock()
		return c.cfg
	}
	c.cfgMu.Lock()
	c.cfg = fresh
	c.cfgMu.Unlock()
	return fresh
}

func (c *InProcessClient) resourceLogPath(res *domain.Resource, spec localconn.ProcessSpec, stream string) (string, error) {
	switch spec.Mode {
	case "", localconn.ProcessModeDevSession:
		def := localconn.ResourceToServiceDef(res)
		if def.LogFile != "" {
			return def.LogFile, nil
		}
		return (&service.ManagedService{Def: def}).LogPath(), nil
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

// ListServices implements Client.
func (c *InProcessClient) ListServices(_ context.Context) ([]ServiceStatus, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.reloadLogged()
	svcs := c.reg.Current()

	var monStatus *daemon.MonitorStatus
	if c.monitor != nil {
		ms := c.monitor.GetStatus()
		monStatus = &ms
	}

	out := make([]ServiceStatus, 0, len(svcs))
	for _, svc := range svcs {
		out = append(out, buildServiceStatus(svc, monStatus))
	}
	return out, nil
}

// GetService implements Client.
func (c *InProcessClient) GetService(_ context.Context, id string) (*ServiceStatus, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	svc := c.reloadAndFind(id)
	if svc == nil {
		return nil, fmt.Errorf("service %q not found", id)
	}
	var monStatus *daemon.MonitorStatus
	if c.monitor != nil {
		ms := c.monitor.GetStatus()
		monStatus = &ms
	}
	status := buildServiceStatus(svc, monStatus)
	return &status, nil
}

// StartService implements Client.
func (c *InProcessClient) StartService(_ context.Context, id string) (*OpResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", id),
		}, nil
	}
	if err := svc.Start(); err != nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("failed to start service %q: %s", id, err.Error()),
		}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("service %q started successfully", id),
	}, nil
}

// StopService implements Client.
func (c *InProcessClient) StopService(_ context.Context, id string, audit AuditContext) (*OpResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", id),
		}, nil
	}
	if svc.Def.Protected {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", id),
		}, nil
	}

	service.LogAudit("stop", id, audit.Reason, audit.TaskID, audit.SessionID)

	// Pause auto-restart so the monitor doesn't undo the stop. Matches
	// the pattern in internal/mcp/tools_lifecycle.go — no defer-resume
	// on stop: a deliberate stop stays stopped until the operator
	// explicitly starts or resumes.
	_ = pausectl.PauseService(id)

	if err := svc.Stop(); err != nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("failed to stop service %q: %s", id, err.Error()),
		}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("service %q stopped successfully (reason: %s)", id, audit.Reason),
	}, nil
}

// RestartService implements Client.
func (c *InProcessClient) RestartService(_ context.Context, id string, args RestartServiceArgs) (*OpResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", id),
		}, nil
	}
	if svc.Def.Protected {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", id),
		}, nil
	}

	service.LogAudit("restart", id, args.Audit.Reason, args.Audit.TaskID, args.Audit.SessionID)

	_ = pausectl.PauseService(id)
	defer func() { _ = pausectl.ResumeService(id) }()

	if err := svc.Stop(); err != nil {
		if !args.Force {
			return &OpResult{
				Success:   false,
				ServiceID: id,
				Error:     fmt.Sprintf("failed to stop service %q during restart: %s (use force=true to proceed anyway)", id, err.Error()),
			}, nil
		}
	}
	if err := svc.Start(); err != nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q stopped but failed to start: %s", id, err.Error()),
		}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("service %q restarted successfully (reason: %s)", id, args.Audit.Reason),
	}, nil
}

// RebuildService implements Client.
func (c *InProcessClient) RebuildService(_ context.Context, id string, args RebuildServiceArgs) (*OpResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", id),
		}, nil
	}
	if svc.Def.Protected {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", id),
		}, nil
	}

	service.LogAudit("rebuild", id, args.Audit.Reason, args.Audit.TaskID, args.Audit.SessionID)

	_ = pausectl.PauseService(id)
	defer func() { _ = pausectl.ResumeService(id) }()

	// Capture the binary fingerprint BEFORE the build step so we can
	// identify processes still running the pre-build inode after
	// `go install` (or equivalent) replaces the file on disk. See
	// internal/procscan for the full rationale (CERB-3).
	fp := procscan.CaptureForService(svc.Def.Command, svc.Def.Dir, c.logger)

	var buildOutput string
	if len(svc.Def.Build) > 0 {
		out, err := svc.BuildSync()
		buildOutput = strings.TrimSpace(out)
		if err != nil && !args.Force {
			return &OpResult{
				Success:     false,
				ServiceID:   id,
				BuildOutput: buildOutput,
				Error:       fmt.Sprintf("build failed for %q: %s (use force=true to restart anyway)", id, err.Error()),
			}, nil
		}
	}

	// Cascade-kill foreign subprocesses still running the pre-build
	// inode. Their parents will respawn against the freshly-installed
	// binary on the next tool call. No-op when fp is zero (e.g. for
	// services whose Command[0] is an interpreter like `go run`).
	//
	// TODO(CERB-followup): cascade-kill targets foreign PIDs and doesn't
	// need opMu protection. Lift this out of the critical section so
	// concurrent unrelated tool calls don't block for ~2.5s grace + kill.
	_ = procscan.CascadeKillStaleSubprocesses(fp, c.logger)

	_ = svc.Stop()
	if err := svc.Start(); err != nil {
		return &OpResult{
			Success:     false,
			ServiceID:   id,
			BuildOutput: buildOutput,
			Error:       fmt.Sprintf("build succeeded but failed to start %q: %s", id, err.Error()),
		}, nil
	}
	return &OpResult{
		Success:     true,
		ServiceID:   id,
		BuildOutput: buildOutput,
		Message:     fmt.Sprintf("service %q rebuilt and restarted successfully (reason: %s)", id, args.Audit.Reason),
	}, nil
}

// BuildService implements Client.
func (c *InProcessClient) BuildService(_ context.Context, id string) (*OpResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("service %q not found", id),
		}, nil
	}
	if len(svc.Def.Build) == 0 {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     "no build command configured for this service",
		}, nil
	}
	output, err := svc.BuildSync()
	if err != nil {
		// Surface build failure as a structured OpResult (success=false,
		// error=<msg>) rather than a transport-level error so the socket
		// server returns 200 OK with the payload intact. This matches
		// the MCP tool contract established in internal/mcp.
		return &OpResult{ //nolint:nilerr // intentional — see comment
			Success:     false,
			ServiceID:   id,
			BuildOutput: output,
			Error:       err.Error(),
		}, nil
	}
	return &OpResult{
		Success:     true,
		ServiceID:   id,
		BuildOutput: output,
	}, nil
}

// ServiceLogs implements Client.
func (c *InProcessClient) ServiceLogs(_ context.Context, id string, lines int) (*LogLines, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	svc := c.reloadAndFind(id)
	if svc == nil {
		return nil, fmt.Errorf("unknown service: %s", id)
	}
	if lines <= 0 {
		lines = 50
	}
	// svc.LogPath() returns the live tempdir path that was assigned on
	// Start(). Before the first Start — or when the process was
	// started in a prior daemon — logPath is unset and LogPath()
	// falls back to the tempdir default. Prefer an explicit
	// Def.LogFile when it's configured so operators can point at a
	// persistent log (e.g. ~/.cerberus/logs/svc.log) and still see
	// output via cerberus_logs before the service starts.
	logPath := svc.Def.LogFile
	if logPath == "" {
		logPath = svc.LogPath()
	}
	if logPath == "" {
		return &LogLines{ServiceID: id, Content: "No log path configured for this service."}, nil
	}
	content, err := readLastNLines(logPath, lines)
	if err != nil {
		if os.IsNotExist(err) {
			return &LogLines{
				ServiceID: id,
				LogPath:   logPath,
				Content:   fmt.Sprintf("Log file does not exist: %s", logPath),
			}, nil
		}
		return nil, fmt.Errorf("failed to read log file: %w", err)
	}
	if content == "" {
		content = fmt.Sprintf("Log file is empty: %s", logPath)
	}
	return &LogLines{ServiceID: id, LogPath: logPath, Content: content}, nil
}

// ResourceLogs implements Client.
func (c *InProcessClient) ResourceLogs(_ context.Context, id string, lines int, stream string) (*LogLines, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return nil, fmt.Errorf("no config available")
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return nil, fmt.Errorf("resource %q not found", id)
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return nil, fmt.Errorf("resource %q is %s/%s; logs currently support local process resources only", res.ID, res.Type, res.Connector)
	}
	if lines <= 0 {
		lines = 50
	}
	if stream == "" {
		stream = "stdout"
	}

	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	logPath, err := c.resourceLogPath(resourceDefToDomain(res), spec, stream)
	if err != nil {
		return nil, err
	}
	if logPath == "" {
		return &LogLines{ResourceID: id, Stream: stream, Content: "No log path configured for this resource."}, nil
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
		return nil, fmt.Errorf("failed to read log file: %w", err)
	}
	if content == "" {
		content = fmt.Sprintf("Log file is empty: %s", logPath)
	}
	return &LogLines{ResourceID: id, Stream: stream, LogPath: logPath, Content: content}, nil
}

// Health implements Client.
func (c *InProcessClient) Health(_ context.Context, id string) (*DaemonHealth, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.reloadLogged()
	svcs := c.reg.Current()

	var entries []ServiceHealth
	for _, svc := range svcs {
		if id != "" && svc.Def.ID != id {
			continue
		}
		hasHealthCheck := svc.Def.HealthCheckCfg.URL != "" || len(svc.Def.HealthCheckCfg.Command) > 0
		entry := ServiceHealth{
			ServiceID:        svc.Def.ID,
			HealthConfigured: hasHealthCheck,
		}
		if !hasHealthCheck {
			entries = append(entries, entry)
			continue
		}
		entry.Healthy = svc.HealthStatus.Healthy
		entry.ConsecutiveFailures = svc.HealthStatus.ConsecutiveFailures
		entry.LastError = svc.HealthStatus.LastError
		if !svc.HealthStatus.LastCheck.IsZero() {
			entry.LastCheck = svc.HealthStatus.LastCheck.Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}

	if id != "" && len(entries) == 0 {
		return nil, fmt.Errorf("unknown service: %s", id)
	}

	resp := &DaemonHealth{Services: entries}
	if c.monitor != nil {
		monStatus := c.monitor.GetStatus()
		resp.DaemonRunning = monStatus.Running
		resp.MonitorInterval = monStatus.CheckInterval.String()
		protectedCount := 0
		failedCount := 0
		for _, svc := range svcs {
			if svc.Def.Protected {
				protectedCount++
			}
			if stats, hasStats := monStatus.ServiceStats[svc.Def.ID]; hasStats {
				maxAttempts := 3
				if svc.Def.MaxRestartAttempts > 0 {
					maxAttempts = svc.Def.MaxRestartAttempts
				}
				if stats.FailureCount >= maxAttempts {
					failedCount++
				}
			}
		}
		resp.ServicesProtected = protectedCount
		resp.ServicesFailed = failedCount
	}
	return resp, nil
}

// ListProjects implements Client.
func (c *InProcessClient) ListProjects(_ context.Context) ([]ProjectInfo, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	counts := make(map[string]int)
	for _, r := range cfg.Resources {
		counts[r.Project]++
	}
	out := make([]ProjectInfo, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		out = append(out, ProjectInfo{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Resources:   counts[p.ID],
		})
	}
	return out, nil
}

// ListResources implements Client.
func (c *InProcessClient) ListResources(ctx context.Context, args ResourceListArgs) ([]ResourceInfo, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	var out []ResourceInfo
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
		info := ResourceInfo{
			ID:         r.ID,
			Name:       r.Name,
			Type:       r.Type,
			Project:    r.Project,
			Connector:  r.Connector,
			Mode:       resourceMode(r),
			Supervisor: resourceSupervisor(r),
			RunFrom:    resourceRunFrom(r),
			Tags:       r.Tags,
		}
		if r.Type == string(domain.ResourceProcess) && r.Connector == "local" {
			spec, _ := localconn.SpecFromResourceConfig(r.Config)
			dr := resourceDefToDomain(&r)
			if state, err := c.localConnector().Status(ctx, dr); err == nil {
				info.Status = string(state)
			}
			if _, art, err := localconn.InspectArtifactInstall(dr, spec); err == nil {
				info.ArtifactInstalled = art.Installed
				info.ArtifactStale = art.Stale
				if info.Status != "" {
					if action, _ := localconn.RecommendedStatusAction(spec, domain.State(info.Status), art); action != "" {
						info.RecommendedAction = action
					}
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// GetResourceRuntime implements Client.
func (c *InProcessClient) GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error) {
	cfg := c.snapshotConfig()
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
	state, err := c.localConnector().Status(ctx, dr)
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
	}
	status := &ResourceRuntimeStatus{
		ID:                  res.ID,
		Name:                res.Name,
		Type:                res.Type,
		Project:             res.Project,
		Connector:           res.Connector,
		Mode:                resourceMode(*res),
		Supervisor:          resourceSupervisor(*res),
		RunFrom:             resourceRunFrom(*res),
		Status:              string(state),
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
	}
	return status, nil
}

// GetResourceInspect implements Client.
func (c *InProcessClient) GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error) {
	cfg := c.snapshotConfig()
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
	state, err := c.localConnector().Status(ctx, dr)
	if err != nil {
		return nil, err
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)

	out := &ResourceInspect{
		ID:           res.ID,
		Name:         res.Name,
		Type:         res.Type,
		Project:      res.Project,
		Connector:    res.Connector,
		Mode:         resourceMode(*res),
		Supervisor:   resourceSupervisor(*res),
		RunFrom:      resourceRunFrom(*res),
		Status:       string(state),
		WorkspaceDir: spec.Dir,
		Command:      append([]string(nil), spec.Command...),
		Build:        append([]string(nil), spec.Build...),
		WorkingDir:   spec.Dir,
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
		}
	}

	return out, nil
}

// ApplyResource implements Client.
func (c *InProcessClient) ApplyResource(ctx context.Context, id string) (*OpResult, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return &OpResult{Success: false, ServiceID: id, Error: "no config available"}, nil
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return &OpResult{Success: false, ServiceID: id, Error: fmt.Sprintf("resource %q not found", id)}, nil
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("resource %q is %s/%s; apply currently supports local process resources only", res.ID, res.Type, res.Connector),
		}, nil
	}

	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	applyRes, startErr := c.localConnector().Apply(ctx, resourceDefToDomain(res))
	if startErr != nil {
		//nolint:nilerr // OpResult carries operator-facing failure details; transport error remains nil
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     startErr.Error(),
		}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   localconn.FormatApplyResultMessage(id, spec, applyRes),
	}, nil
}

// SyncResource implements Client.
func (c *InProcessClient) SyncResource(_ context.Context, id string) (*OpResult, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return &OpResult{Success: false, ServiceID: id, Error: "no config available"}, nil
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return &OpResult{Success: false, ServiceID: id, Error: fmt.Sprintf("resource %q not found", id)}, nil
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("resource %q is %s/%s; sync currently supports local process resources only", res.ID, res.Type, res.Connector),
		}, nil
	}
	spec, _ := localconn.SpecFromResourceConfig(res.Config)
	if spec.RunFrom != localconn.ProcessRunFromArtifact {
		return &OpResult{
			Success:   true,
			ServiceID: id,
			Message:   fmt.Sprintf("resource %q does not use artifact mode; nothing to sync", id),
		}, nil
	}
	_, syncRes, err := localconn.SyncArtifactInstall(resourceDefToDomain(res), spec)
	if err != nil {
		return &OpResult{ //nolint:nilerr // OpResult carries operator-facing failure details; transport error remains nil
			Success:   false,
			ServiceID: id,
			Error:     err.Error(),
		}, nil
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

// RemoveResource implements Client.
func (c *InProcessClient) RemoveResource(ctx context.Context, id string) (*OpResult, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return &OpResult{Success: false, ServiceID: id, Error: "no config available"}, nil
	}
	res := findResourceDef(cfg, id)
	if res == nil {
		return &OpResult{Success: false, ServiceID: id, Error: fmt.Sprintf("resource %q not found", id)}, nil
	}
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return &OpResult{
			Success:   false,
			ServiceID: id,
			Error:     fmt.Sprintf("resource %q is %s/%s; remove currently supports local process resources only", res.ID, res.Type, res.Connector),
		}, nil
	}
	if err := c.localConnector().Destroy(ctx, resourceDefToDomain(res)); err != nil {
		return &OpResult{ //nolint:nilerr // OpResult carries operator-facing failure details; transport error remains nil
			Success:   false,
			ServiceID: id,
			Error:     err.Error(),
		}, nil
	}
	return &OpResult{
		Success:   true,
		ServiceID: id,
		Message:   fmt.Sprintf("resource %q removed successfully", id),
	}, nil
}

func (c *InProcessClient) localConnector() *localconn.Connector {
	if c.local != nil {
		return c.local
	}
	return localconn.New()
}

// ListPipelines implements Client.
func (c *InProcessClient) ListPipelines(_ context.Context) ([]PipelineInfo, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return nil, nil
	}
	out := make([]PipelineInfo, 0, len(cfg.Pipelines))
	for _, p := range cfg.Pipelines {
		out = append(out, PipelineInfo{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Stages:      len(p.Stages),
		})
	}
	return out, nil
}

// RunPipeline implements Client.
func (c *InProcessClient) RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error) {
	cfg := c.snapshotConfig()
	if cfg == nil {
		return &PipelineRunResult{Success: false, Error: "no config available"}, nil
	}
	var pdef *config.PipelineDef
	for i := range cfg.Pipelines {
		if cfg.Pipelines[i].ID == id {
			pdef = &cfg.Pipelines[i]
			break
		}
	}
	if pdef == nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline %q not found in config", id)}, nil
	}

	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.reloadLogged()
	p, err := pipeline.Resolve(*pdef, c.reg.Current(), c.local)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("resolve pipeline: %s", err.Error())}, nil
	}
	env := &domain.PipelineEnv{Values: make(map[string]any)}
	exec := pipeline.NewExecutor(nil)
	result, err := exec.Run(ctx, p, env)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline execution: %s", err.Error())}, nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("marshal result: %s", err.Error())}, nil
	}
	return &PipelineRunResult{Success: true, Raw: raw}, nil
}

// buildServiceStatus renders a single ManagedService into the DTO used
// by ListServices / GetService. Factored out so the socket server and
// the in-process client produce bit-identical output.
func buildServiceStatus(svc *service.ManagedService, monStatus *daemon.MonitorStatus) ServiceStatus {
	svc.Poll()

	var uptimeSeconds float64
	if svc.Status != service.StatusStopped && !svc.Uptime.IsZero() {
		uptimeSeconds = time.Since(svc.Uptime).Seconds()
	}

	health := ""
	if svc.HealthStatus.LastCheck.IsZero() {
		health = "unknown"
	} else if svc.HealthStatus.Healthy {
		health = "healthy"
	} else {
		health = "unhealthy"
	}

	entry := ServiceStatus{
		ID:            svc.Def.ID,
		Name:          svc.Def.Name,
		Status:        svc.Status.String(),
		PID:           svc.PID,
		Port:          svc.Def.Port,
		UptimeSeconds: uptimeSeconds,
		Health:        health,
		Error:         svc.Error,
		Protected:     svc.Def.Protected,
		AutoRestart:   svc.Def.AutoRestart,
		Stale:         svc.Stale,
		LogPath:       svc.LogPath(),
		HasBuild:      len(svc.Def.Build) > 0,
	}

	if monStatus != nil {
		if stats, ok := monStatus.ServiceStats[svc.Def.ID]; ok {
			entry.RestartCount = stats.FailureCount
			if stats.LastRestart != nil {
				entry.LastRestartAt = stats.LastRestart.Format(time.RFC3339)
			}
		}
		maxAttempts := 3
		if svc.Def.MaxRestartAttempts > 0 {
			maxAttempts = svc.Def.MaxRestartAttempts
		}
		entry.DaemonState = deriveDaemonState(svc, monStatus, maxAttempts)
	}

	return entry
}

// deriveDaemonState mirrors the internal/mcp helper of the same name.
// Kept local so the cerbapi package doesn't cross-import internal/mcp.
func deriveDaemonState(svc *service.ManagedService, monStatus *daemon.MonitorStatus, maxAttempts int) string {
	if !svc.Def.AutoRestart && !svc.Def.Protected {
		return "unmanaged"
	}
	stats, hasStats := monStatus.ServiceStats[svc.Def.ID]
	switch svc.Status {
	case service.StatusRunning, service.StatusHealthy:
		return "healthy"
	case service.StatusStopped:
		if hasStats && stats.FailureCount >= maxAttempts {
			return "failed"
		}
		return "stopped"
	case service.StatusFailed:
		if hasStats && stats.FailureCount >= maxAttempts {
			return "failed"
		}
		if hasStats && stats.FailureCount > 0 {
			return "restarting"
		}
		return "failed"
	default:
		return "unknown"
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

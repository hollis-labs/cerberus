package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/chrispian/cerberus/internal/pipeline"
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
	cfg     *config.ConfigV2
	local   *localconn.Connector
}

// InProcessOption tunes construction of an InProcessClient.
type InProcessOption func(*InProcessClient)

// WithMonitor attaches a daemon monitor so Health/Status can surface
// restart stats. Optional — callers without a monitor pass nothing.
func WithMonitor(m *daemon.Monitor) InProcessOption {
	return func(c *InProcessClient) { c.monitor = m }
}

// WithConfigV2 attaches the v2 config snapshot used by project / resource
// / pipeline list endpoints. Without it those endpoints return empty
// lists.
func WithConfigV2(cfg *config.ConfigV2) InProcessOption {
	return func(c *InProcessClient) { c.cfg = cfg }
}

// WithLocalConnector attaches the local connector used by pipeline
// execution. Required for RunPipeline; unused otherwise.
func WithLocalConnector(l *localconn.Connector) InProcessOption {
	return func(c *InProcessClient) { c.local = l }
}

// NewInProcessClient constructs an InProcessClient. reg is required; all
// other collaborators are optional (see WithMonitor, WithConfigV2,
// WithLocalConnector).
func NewInProcessClient(reg *service.ServiceRegistry, opts ...InProcessOption) *InProcessClient {
	c := &InProcessClient{reg: reg}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// reloadAndFind refreshes the registry from disk before resolving. This
// mirrors internal/mcp.reloadAndFind — the sole purpose of this method
// is to ensure every lifecycle op sees the current on-disk config.
func (c *InProcessClient) reloadAndFind(id string) *service.ManagedService {
	_ = c.reg.Reload()
	return c.reg.Find(id)
}

// ListServices implements Client.
func (c *InProcessClient) ListServices(_ context.Context) ([]ServiceStatus, error) {
	_ = c.reg.Reload()
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
	svc := c.reloadAndFind(id)
	if svc == nil {
		return nil, fmt.Errorf("unknown service: %s", id)
	}
	if lines <= 0 {
		lines = 50
	}
	logPath := svc.LogPath()
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

// Health implements Client.
func (c *InProcessClient) Health(_ context.Context, id string) (*DaemonHealth, error) {
	_ = c.reg.Reload()
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
	if c.cfg == nil {
		return nil, nil
	}
	counts := make(map[string]int)
	for _, r := range c.cfg.Resources {
		counts[r.Project]++
	}
	out := make([]ProjectInfo, 0, len(c.cfg.Projects))
	for _, p := range c.cfg.Projects {
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
func (c *InProcessClient) ListResources(_ context.Context, args ResourceListArgs) ([]ResourceInfo, error) {
	if c.cfg == nil {
		return nil, nil
	}
	var out []ResourceInfo
	for _, r := range c.cfg.Resources {
		if args.ProjectID != "" && r.Project != args.ProjectID {
			continue
		}
		if args.Connector != "" && r.Connector != args.Connector {
			continue
		}
		if args.Tag != "" && !containsTagFold(r.Tags, args.Tag) {
			continue
		}
		out = append(out, ResourceInfo{
			ID:        r.ID,
			Name:      r.Name,
			Type:      r.Type,
			Project:   r.Project,
			Connector: r.Connector,
			Tags:      r.Tags,
		})
	}
	return out, nil
}

// ListPipelines implements Client.
func (c *InProcessClient) ListPipelines(_ context.Context) ([]PipelineInfo, error) {
	if c.cfg == nil {
		return nil, nil
	}
	out := make([]PipelineInfo, 0, len(c.cfg.Pipelines))
	for _, p := range c.cfg.Pipelines {
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
	if c.cfg == nil {
		return &PipelineRunResult{Success: false, Error: "no config available"}, nil
	}
	var pdef *config.PipelineDef
	for i := range c.cfg.Pipelines {
		if c.cfg.Pipelines[i].ID == id {
			pdef = &c.cfg.Pipelines[i]
			break
		}
	}
	if pdef == nil {
		return &PipelineRunResult{Success: false, Error: fmt.Sprintf("pipeline %q not found in config", id)}, nil
	}

	_ = c.reg.Reload()
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

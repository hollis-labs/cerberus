// Package cerbapi defines the abstract Cerberus RPC interface used by
// callers (CLI, MCP subprocess, daemon-internal MCP handlers) to drive
// service lifecycle operations.
//
// Two implementations satisfy the Client interface:
//
//   - InProcessClient: runs ops against a live *service.ServiceRegistry
//     (used by the daemon itself and by daemon-embedded MCP handlers).
//   - SocketClient: dials a unix-socket HTTP endpoint served by the
//     daemon (used by the standalone `cerberus mcp` subprocess and,
//     optionally, by short-lived CLI commands).
//
// The goal is a single source of truth for service state: the daemon's
// in-memory ServiceRegistry. Spawned MCP subprocesses no longer cache
// config, so they cannot serve stale definitions — every tool call
// forwards to the daemon, which re-reads config via its Reload() path
// (CERB-1) before executing the op.
package cerbapi

import "time"

// APIHeaderName is the protocol-version header sent by clients and
// validated by the server. Bumping this is a breaking change; there is
// intentionally no migration logic (see CERB-2 non-goals).
const APIHeaderName = "X-Cerberus-Api"

// APIVersion is the single supported protocol version.
const APIVersion = "v1"

// ServiceStatus is the DTO for a single service's runtime state. Mirrors
// the JSON shape of the existing cerberus_status tool so MCP output is
// unchanged when routing through the socket.
type ServiceStatus struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	PID           int     `json:"pid"`
	Port          int     `json:"port"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Health        string  `json:"health"`
	Error         string  `json:"error,omitempty"`
	Protected     bool    `json:"protected"`
	AutoRestart   bool    `json:"auto_restart"`
	RestartCount  int     `json:"restart_count,omitempty"`
	LastRestartAt string  `json:"last_restart_at,omitempty"`
	DaemonState   string  `json:"daemon_state,omitempty"`
	// Stale mirrors ManagedService.Stale — the on-disk definition has
	// changed since this process was started. Clears on next successful
	// Start/Restart/Rebuild.
	Stale bool `json:"stale,omitempty"`
	// LogPath is the absolute path to the service's log file. Exposed
	// here (rather than as a separate endpoint) so log readers can
	// resolve the path against the daemon's view of the config.
	LogPath string `json:"log_path,omitempty"`
	// HasBuild is true when the service definition has a build command.
	HasBuild bool `json:"has_build,omitempty"`
}

// AuditContext captures who requested a destructive lifecycle op and
// why. Required for stop/restart/rebuild.
type AuditContext struct {
	Reason    string `json:"reason"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// OpResult is the DTO for a lifecycle-operation response.
type OpResult struct {
	Success     bool   `json:"success"`
	ServiceID   string `json:"service_id"`
	Message     string `json:"message,omitempty"`
	BuildOutput string `json:"build_output,omitempty"`
	Error       string `json:"error,omitempty"`
}

// RestartServiceArgs wraps RestartService + RebuildService optional
// behaviors. Kept simple intentionally — only fields already used by
// the MCP tools are wired.
type RestartServiceArgs struct {
	Audit AuditContext
	Force bool
}

// RebuildServiceArgs ditto.
type RebuildServiceArgs struct {
	Audit AuditContext
	Force bool
}

// ServiceHealth is the DTO for a single service's health state.
type ServiceHealth struct {
	ServiceID           string `json:"service_id"`
	HealthConfigured    bool   `json:"health_configured"`
	Healthy             bool   `json:"healthy"`
	LastCheck           string `json:"last_check,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

// DaemonHealth aggregates daemon + per-service health snapshots.
type DaemonHealth struct {
	Services          []ServiceHealth `json:"services"`
	DaemonRunning     bool            `json:"daemon_running"`
	MonitorInterval   string          `json:"monitor_interval,omitempty"`
	ServicesProtected int             `json:"services_protected"`
	ServicesFailed    int             `json:"services_failed"`
}

// LogLines is the DTO for tail-logs responses.
type LogLines struct {
	ServiceID  string `json:"service_id,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Stream     string `json:"stream,omitempty"`
	Content    string `json:"content"`
	LogPath    string `json:"log_path"`
}

// ErrorResponse is the JSON body emitted by the socket server when a
// handler returns an error. Clients decode this into a typed error.
type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// ProjectInfo is the DTO for project-list responses.
type ProjectInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Resources   int    `json:"resource_count"`
}

// ResourceInfo is the DTO for resource-list responses.
type ResourceInfo struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Project           string   `json:"project"`
	Connector         string   `json:"connector"`
	Mode              string   `json:"mode,omitempty"`
	Supervisor        string   `json:"supervisor,omitempty"`
	RunFrom           string   `json:"run_from,omitempty"`
	Status            string   `json:"status,omitempty"`
	ArtifactInstalled bool     `json:"artifact_installed,omitempty"`
	ArtifactStale     bool     `json:"artifact_stale,omitempty"`
	RecommendedAction string   `json:"recommended_action,omitempty"`
	Tags              []string `json:"tags,omitempty"`
}

// ResourceListArgs filters the resource list response.
type ResourceListArgs struct {
	ProjectID string
	Connector string
	Tag       string
}

// ResourceRuntimeStatus is the DTO for resource-runtime status and apply flows.
type ResourceRuntimeStatus struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	Project             string `json:"project"`
	Connector           string `json:"connector"`
	Mode                string `json:"mode,omitempty"`
	Supervisor          string `json:"supervisor,omitempty"`
	RunFrom             string `json:"run_from,omitempty"`
	Status              string `json:"status"`
	ServiceName         string `json:"service_name,omitempty"`
	ArtifactPath        string `json:"artifact_path,omitempty"`
	InstallRoot         string `json:"install_root,omitempty"`
	ArtifactInstalled   bool   `json:"artifact_installed,omitempty"`
	ArtifactStale       bool   `json:"artifact_stale,omitempty"`
	ArtifactStaleReason string `json:"artifact_stale_reason,omitempty"`
	ArtifactSource      string `json:"artifact_source,omitempty"`
	ArtifactSyncedAt    string `json:"artifact_synced_at,omitempty"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
	RecommendedReason   string `json:"recommended_reason,omitempty"`
}

// ResourceInspect is the detailed operator-facing inspection view for a local process resource.
type ResourceInspect struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Project             string   `json:"project"`
	Connector           string   `json:"connector"`
	Mode                string   `json:"mode,omitempty"`
	Supervisor          string   `json:"supervisor,omitempty"`
	RunFrom             string   `json:"run_from,omitempty"`
	Status              string   `json:"status,omitempty"`
	WorkspaceDir        string   `json:"workspace_dir,omitempty"`
	WorkingDir          string   `json:"working_dir,omitempty"`
	Command             []string `json:"command,omitempty"`
	Build               []string `json:"build,omitempty"`
	ServiceName         string   `json:"service_name,omitempty"`
	PlistPath           string   `json:"plist_path,omitempty"`
	InstallRoot         string   `json:"install_root,omitempty"`
	InstallWorkDir      string   `json:"install_work_dir,omitempty"`
	BinDir              string   `json:"bin_dir,omitempty"`
	ArtifactPath        string   `json:"artifact_path,omitempty"`
	ArtifactInstalled   bool     `json:"artifact_installed,omitempty"`
	ArtifactStale       bool     `json:"artifact_stale,omitempty"`
	ArtifactStaleReason string   `json:"artifact_stale_reason,omitempty"`
	ArtifactSource      string   `json:"artifact_source,omitempty"`
	ArtifactSyncedAt    string   `json:"artifact_synced_at,omitempty"`
	StdoutLogPath       string   `json:"stdout_log_path,omitempty"`
	StderrLogPath       string   `json:"stderr_log_path,omitempty"`
	RecommendedAction   string   `json:"recommended_action,omitempty"`
	RecommendedReason   string   `json:"recommended_reason,omitempty"`
}

// PipelineInfo is the DTO for pipeline-list responses.
type PipelineInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Stages      int    `json:"stage_count"`
}

// PipelineRunResult is the DTO for pipeline-run responses. The body is
// whatever pipeline.Executor returns, serialized verbatim, so we don't
// have to re-declare the nested structure here.
type PipelineRunResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	// Raw is the JSON body of the underlying pipeline.Result, passed
	// through opaquely to avoid coupling cerbapi to pipeline internals.
	Raw []byte `json:"raw,omitempty"`
}

// DialTimeout is the default deadline for establishing a socket
// connection. Kept small: the daemon is either reachable locally or not.
const DialTimeout = 2 * time.Second

// RequestTimeout is the default deadline for a single RPC round-trip.
// Lifecycle ops (Start, Stop) can internally block up to ~10s for SIGKILL
// escalation; give the server generous headroom.
const RequestTimeout = 30 * time.Second

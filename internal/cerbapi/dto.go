// Package cerbapi defines the abstract Cerberus RPC interface used by
// callers (CLI, MCP subprocess, daemon-internal MCP handlers) to drive
// service lifecycle operations and the v2 resource runtime surface.
//
// Two implementations satisfy the Client interface:
//
//   - InProcessClient: runs ops against the daemon's shared runtime
//     services (used by the daemon itself and by daemon-embedded MCP
//     handlers).
//   - SocketClient: dials a unix-socket HTTP endpoint served by the
//     daemon (used by the standalone `cerberus mcp` subprocess and,
//     optionally, by short-lived CLI commands).
//
// The goal is a single source of truth per runtime lane:
//
//   - legacy v1 service state is still owned by the daemon's in-memory
//     ServiceRegistry.
//   - v2 resource runtime operations are owned by the shared
//     ResourceRuntimeService.
//
// Spawned MCP subprocesses no longer cache config, so they cannot serve
// stale definitions — every tool call forwards to the daemon, which
// routes to the appropriate shared runtime layer before executing the op.
package cerbapi

import (
	"encoding/json"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// APIHeaderName is the protocol-version header sent by clients and
// validated by the server. Bumping this is a breaking change; there is
// intentionally no migration logic (see CERB-2 non-goals).
const APIHeaderName = "X-Cerberus-Api"

// APIVersion is the single supported protocol version.
const APIVersion = "v1"

const ProgressHeaderName = "X-Cerberus-Progress"

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

// DeployResourceOpts carries per-invocation overrides for DeployResource.
// Construct with functional options (WithInstallAfterBuildOverride, etc.) and
// pass through Client.DeployResource. Socket transport serializes these to the
// /resources/{id}/deploy request body so daemon-routed CLIs see the same
// precedence layering as in-process callers.
type DeployResourceOpts struct {
	// InstallAfterBuildOverride forces install_after_build behavior for this
	// invocation when non-nil. Highest-precedence layer; corresponds to the
	// --install-after-build / --no-install-after-build CLI flags.
	InstallAfterBuildOverride *bool `json:"install_after_build_override,omitempty"`
}

// DeployResourceOption is a functional option for DeployResource.
type DeployResourceOption func(*DeployResourceOpts)

// WithInstallAfterBuildOverride sets the per-invocation install_after_build
// override. The value travels at the highest precedence in the resolver,
// beating both the resource-level setting and the global default.
func WithInstallAfterBuildOverride(v bool) DeployResourceOption {
	return func(o *DeployResourceOpts) { o.InstallAfterBuildOverride = &v }
}

// ApplyDeployResourceOptions folds a slice of options into a value-typed opts
// struct. Useful for callers that need to forward options over the socket
// boundary where functional options can't survive.
func ApplyDeployResourceOptions(options []DeployResourceOption) DeployResourceOpts {
	var opts DeployResourceOpts
	for _, o := range options {
		if o != nil {
			o(&opts)
		}
	}
	return opts
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

// ResourceHealth is the DTO for a single v2 resource runtime health snapshot.
type ResourceHealth struct {
	ResourceID          string `json:"resource_id"`
	Status              string `json:"status"`
	Healthy             bool   `json:"healthy"`
	OperatorStopped     bool   `json:"operator_stopped,omitempty"`
	Mode                string `json:"mode,omitempty"`
	Supervisor          string `json:"supervisor,omitempty"`
	RunFrom             string `json:"run_from,omitempty"`
	ArtifactInstalled   bool   `json:"artifact_installed,omitempty"`
	ArtifactStale       bool   `json:"artifact_stale,omitempty"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
	RecommendedReason   string `json:"recommended_reason,omitempty"`
	RecommendedNextStep string `json:"recommended_next_step,omitempty"`
}

// DaemonHealth aggregates daemon + per-service health snapshots.
type DaemonHealth struct {
	Services          []ServiceHealth  `json:"services"`
	Resources         []ResourceHealth `json:"resources,omitempty"`
	DaemonRunning     bool             `json:"daemon_running"`
	MonitorInterval   string           `json:"monitor_interval,omitempty"`
	ServicesProtected int              `json:"services_protected"`
	ServicesFailed    int              `json:"services_failed"`
}

// DaemonStatus is a lightweight daemon/socket liveness snapshot used by
// operator-facing status checks. It intentionally avoids runtime-wide health
// work so "daemon reachable" is distinct from "all resources healthy".
type DaemonStatus struct {
	DaemonRunning bool   `json:"daemon_running"`
	SocketPath    string `json:"socket_path,omitempty"`
	SocketReady   bool   `json:"socket_ready"`
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

type StreamEnvelope struct {
	Type         string             `json:"type"`
	Notification *gmcp.Notification `json:"notification,omitempty"`
	Result       json.RawMessage    `json:"result,omitempty"`
	Error        string             `json:"error,omitempty"`
}

// ResolveDiagnostics is the DTO for what registry resolution dropped or
// complained about on the most recent read of the config tree.
//
// It is a sibling of the list DTOs rather than a field on them: the
// list wire shapes are bare arrays consumed by the CLI, MCP and the
// console, and the point of this record is to let a caller say "the
// list is short because 2 configs were skipped" without changing any
// of them.
type ResolveDiagnostics struct {
	// Skipped counts registered configs dropped from the resolved
	// config because their file is missing or fails validation.
	Skipped int `json:"skipped"`
	// Warned counts registered configs that resolved but carry
	// warning-severity validation issues.
	Warned int `json:"warned"`
	// SkippedOwners and WarnedOwners name the configs behind the
	// counts, so a caller can point at one without a second round trip.
	SkippedOwners []string `json:"skipped_owners,omitempty"`
	WarnedOwners  []string `json:"warned_owners,omitempty"`
}

// Clean reports whether resolution dropped nothing and complained about
// nothing — the case where list output needs no trailing notice.
func (d ResolveDiagnostics) Clean() bool { return d.Skipped == 0 && d.Warned == 0 }

// ProjectInfo is the DTO for project-list responses.
type ProjectInfo struct {
	// ID is the portfolio-wide project slug — the value Tachyon and
	// anything else joins on. See config.ProjectDef.
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Resources   int    `json:"resource_count"`
	// Capabilities and Links are the app-owned portable props, carried
	// verbatim from the project config. config.Link is reused rather
	// than remapped so the JSON is literally Tether's registry shape.
	Capabilities []string      `json:"capabilities,omitempty"`
	Links        []config.Link `json:"links,omitempty"`
}

// ResourceInfo is the DTO for resource-list responses.
type ResourceInfo struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Project             string   `json:"project"`
	Connector           string   `json:"connector"`
	Mode                string   `json:"mode,omitempty"`
	Supervisor          string   `json:"supervisor,omitempty"`
	RunFrom             string   `json:"run_from,omitempty"`
	URL                 string   `json:"url,omitempty"`
	Port                int      `json:"port,omitempty"`
	HasBuild            bool     `json:"has_build,omitempty"`
	Status              string   `json:"status,omitempty"`
	OperatorStopped     bool     `json:"operator_stopped,omitempty"`
	ArtifactInstalled   bool     `json:"artifact_installed,omitempty"`
	ArtifactStale       bool     `json:"artifact_stale,omitempty"`
	RecommendedAction   string   `json:"recommended_action,omitempty"`
	RecommendedNextStep string   `json:"recommended_next_step,omitempty"`
	Tags                []string `json:"tags,omitempty"`
}

// ResourceListArgs filters the resource list response.
type ResourceListArgs struct {
	ProjectID string
	Connector string
	Tag       string
}

// ResourceRuntimeStatus is the DTO for resource-runtime status and apply flows.
type ResourceRuntimeStatus struct {
	ConfigWarnings      []string `json:"config_warnings,omitempty"`
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Project             string   `json:"project"`
	Connector           string   `json:"connector"`
	Mode                string   `json:"mode,omitempty"`
	Supervisor          string   `json:"supervisor,omitempty"`
	RunFrom             string   `json:"run_from,omitempty"`
	URL                 string   `json:"url,omitempty"`
	Port                int      `json:"port,omitempty"`
	HasBuild            bool     `json:"has_build,omitempty"`
	Status              string   `json:"status"`
	OperatorStopped     bool     `json:"operator_stopped,omitempty"`
	ServiceName         string   `json:"service_name,omitempty"`
	ArtifactPath        string   `json:"artifact_path,omitempty"`
	InstallRoot         string   `json:"install_root,omitempty"`
	ArtifactInstalled   bool     `json:"artifact_installed,omitempty"`
	ArtifactStale       bool     `json:"artifact_stale,omitempty"`
	ArtifactStaleReason string   `json:"artifact_stale_reason,omitempty"`
	ArtifactSource      string   `json:"artifact_source,omitempty"`
	ArtifactSyncedAt    string   `json:"artifact_synced_at,omitempty"`
	RecommendedAction   string   `json:"recommended_action,omitempty"`
	RecommendedReason   string   `json:"recommended_reason,omitempty"`
	RecommendedNextStep string   `json:"recommended_next_step,omitempty"`
	LaunchdLoaded       bool     `json:"launchd_loaded,omitempty"`
	LaunchdState        string   `json:"launchd_state,omitempty"`
	LaunchdPID          int      `json:"launchd_pid,omitempty"`
	LaunchdLastExitCode *int     `json:"launchd_last_exit_code,omitempty"`
	LaunchdThrottled    bool     `json:"launchd_throttled,omitempty"`
	LaunchdReason       string   `json:"launchd_reason,omitempty"`
	LaunchdDiagnosis    string   `json:"launchd_diagnosis,omitempty"`
	LaunchdHighlights   []string `json:"launchd_highlights,omitempty"`
}

// ResourceInspect is the detailed operator-facing inspection view for a local process resource.
type ResourceInspect struct {
	ConfigWarnings      []string `json:"config_warnings,omitempty"`
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Project             string   `json:"project"`
	Connector           string   `json:"connector"`
	Mode                string   `json:"mode,omitempty"`
	Supervisor          string   `json:"supervisor,omitempty"`
	RunFrom             string   `json:"run_from,omitempty"`
	URL                 string   `json:"url,omitempty"`
	Port                int      `json:"port,omitempty"`
	Status              string   `json:"status,omitempty"`
	OperatorStopped     bool     `json:"operator_stopped,omitempty"`
	WorkspaceDir        string   `json:"workspace_dir,omitempty"`
	WorkingDir          string   `json:"working_dir,omitempty"`
	Command             []string `json:"command,omitempty"`
	BuildStrategy       string   `json:"build_strategy,omitempty"`
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
	RecommendedNextStep string   `json:"recommended_next_step,omitempty"`
	LaunchdLoaded       bool     `json:"launchd_loaded,omitempty"`
	LaunchdState        string   `json:"launchd_state,omitempty"`
	LaunchdPID          int      `json:"launchd_pid,omitempty"`
	LaunchdLastExitCode *int     `json:"launchd_last_exit_code,omitempty"`
	LaunchdThrottled    bool     `json:"launchd_throttled,omitempty"`
	LaunchdReason       string   `json:"launchd_reason,omitempty"`
	LaunchdDiagnosis    string   `json:"launchd_diagnosis,omitempty"`
	LaunchdHighlights   []string `json:"launchd_highlights,omitempty"`
	LaunchdRaw          string   `json:"launchd_raw,omitempty"`
}

type ResourceDoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ResourceDoctor struct {
	ResourceID          string                `json:"resource_id"`
	Status              string                `json:"status,omitempty"`
	OperatorStopped     bool                  `json:"operator_stopped,omitempty"`
	Summary             string                `json:"summary"`
	RecommendedAction   string                `json:"recommended_action,omitempty"`
	RecommendedReason   string                `json:"recommended_reason,omitempty"`
	RecommendedNextStep string                `json:"recommended_next_step,omitempty"`
	Checks              []ResourceDoctorCheck `json:"checks"`
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

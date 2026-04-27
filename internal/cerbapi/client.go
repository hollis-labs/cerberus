package cerbapi

import (
	"context"
	"os"
	"path/filepath"
)

// Client is the abstract interface every caller uses to drive Cerberus
// service lifecycle ops.
//
// Implementations must be safe for concurrent use. See InProcessClient
// and SocketClient.
type Client interface {
	// ListServices returns the live service status list.
	ListServices(ctx context.Context) ([]ServiceStatus, error)
	// GetService returns a single service's status, or an error if the
	// service is not registered.
	GetService(ctx context.Context, id string) (*ServiceStatus, error)

	// StartService starts a stopped service. Audit context is optional
	// for start (non-destructive) but recommended.
	StartService(ctx context.Context, id string) (*OpResult, error)
	// StopService sends SIGTERM (escalating to SIGKILL after 10s).
	// Audit fields are required at the MCP tool layer but not enforced
	// here — callers wire the gate.
	StopService(ctx context.Context, id string, audit AuditContext) (*OpResult, error)
	// RestartService stops-then-starts without rebuilding.
	RestartService(ctx context.Context, id string, args RestartServiceArgs) (*OpResult, error)
	// RebuildService builds synchronously, then stops-and-starts.
	RebuildService(ctx context.Context, id string, args RebuildServiceArgs) (*OpResult, error)
	// BuildService runs the service's build command synchronously and
	// returns the combined output.
	BuildService(ctx context.Context, id string) (*OpResult, error)

	// ServiceLogs returns the last N lines of the service log.
	ServiceLogs(ctx context.Context, id string, lines int) (*LogLines, error)
	// ResourceLogs returns the last N lines of the resource log for a given stream.
	ResourceLogs(ctx context.Context, id string, lines int, stream string) (*LogLines, error)

	// Health returns daemon + per-service health state. If id is empty,
	// all services are included.
	Health(ctx context.Context, id string) (*DaemonHealth, error)

	// ListProjects returns project-list output from the daemon's view
	// of the v2 config.
	ListProjects(ctx context.Context) ([]ProjectInfo, error)
	// ListResources returns resource-list output with optional filters.
	ListResources(ctx context.Context, args ResourceListArgs) ([]ResourceInfo, error)
	// GetResourceRuntime returns the runtime status of a specific resource.
	GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error)
	// GetResourceInspect returns detailed inspection output for a specific resource.
	GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error)
	// ApplyResource applies a specific resource through its runtime backend.
	ApplyResource(ctx context.Context, id string) (*OpResult, error)
	// SyncResource syncs installed runtime artifacts without applying the backend.
	SyncResource(ctx context.Context, id string) (*OpResult, error)
	// RemoveResource removes a specific resource from its runtime backend.
	RemoveResource(ctx context.Context, id string) (*OpResult, error)
	// ListPipelines returns pipeline-list output.
	ListPipelines(ctx context.Context) ([]PipelineInfo, error)
	// RunPipeline executes a pipeline and returns the raw result JSON.
	RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error)
}

// SocketPath returns the default cerberus socket path
// (~/.cerberus/cerberus.sock). Mirrors the daemonDir / DaemonPIDPath
// pattern used elsewhere in the codebase.
func SocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	return filepath.Join(dir, "cerberus.sock"), nil
}

// SocketPathAt returns a socket path rooted at a custom base directory.
// Used by tests that want per-process isolation.
func SocketPathAt(base string) (string, error) {
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	return filepath.Join(dir, "cerberus.sock"), nil
}

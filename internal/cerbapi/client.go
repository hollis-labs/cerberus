package cerbapi

import (
	"context"
	"os"
	"path/filepath"
)

// Client is the abstract interface every caller uses to drive Cerberus
// runtime operations.
//
// Implementations must be safe for concurrent use. See InProcessClient
// and SocketClient.
type Client interface {
	// ResourceLogs returns the last N lines of the resource log for a given stream.
	ResourceLogs(ctx context.Context, id string, lines int, stream string) (*LogLines, error)

	// Health returns daemon + runtime health state. If id is empty, all
	// known v2 resources are included.
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
	// GetResourceDoctor returns explicit runtime/install checks for a specific resource.
	GetResourceDoctor(ctx context.Context, id string) (*ResourceDoctor, error)
	// DeployResource runs the declared build contract for a resource, then applies it.
	DeployResource(ctx context.Context, id string) (*OpResult, error)
	// ApplyResource applies a specific resource through its runtime backend.
	ApplyResource(ctx context.Context, id string) (*OpResult, error)
	// ReloadResource asks the runtime backend to restart or kickstart the current installed resource without reinstalling it.
	ReloadResource(ctx context.Context, id string) (*OpResult, error)
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

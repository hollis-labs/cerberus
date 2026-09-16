package cerbapi

import (
	"context"
	"os"
	"path/filepath"

	contract "github.com/chrispian/cerberus/pkg/connector"
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
	// ResolveDiagnostics reports what registry resolution dropped or
	// warned about, so a caller rendering a list can say the list is
	// short rather than showing fewer rows with no explanation.
	ResolveDiagnostics(ctx context.Context) (*ResolveDiagnostics, error)
	// GetResourceRuntime returns the runtime status of a specific resource.
	GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error)
	// GetResourceInspect returns detailed inspection output for a specific resource.
	GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error)
	// GetResourceDoctor returns explicit runtime/install checks for a specific resource.
	GetResourceDoctor(ctx context.Context, id string) (*ResourceDoctor, error)
	// DeployResource runs the declared build contract for a resource, then applies it.
	// Variadic options carry per-invocation overrides (e.g. install_after_build).
	DeployResource(ctx context.Context, id string, opts ...DeployResourceOption) (*OpResult, error)
	// ApplyResource applies a specific resource through its runtime backend.
	ApplyResource(ctx context.Context, id string) (*OpResult, error)
	// ReloadResource asks the runtime backend to restart or kickstart the current installed resource without reinstalling it.
	ReloadResource(ctx context.Context, id string) (*OpResult, error)
	// StopResource stops a resource without removing install state.
	StopResource(ctx context.Context, id string) (*OpResult, error)
	// SyncResource syncs installed runtime artifacts without applying the backend.
	SyncResource(ctx context.Context, id string) (*OpResult, error)
	// RemoveResource removes a specific resource from its runtime backend.
	RemoveResource(ctx context.Context, id string) (*OpResult, error)
	// ListPipelines returns pipeline-list output.
	ListPipelines(ctx context.Context) ([]PipelineInfo, error)
	// GetPipeline returns the definition and validation diagnostics, or nil if missing.
	GetPipeline(ctx context.Context, id string) (*PipelineDetail, error)
	// RunPipeline executes a pipeline and returns the raw result JSON.
	RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error)
	// ListConnectors returns connector discovery metadata.
	ListConnectors(ctx context.Context) ([]contract.Definition, error)

	// ListLiveConnectors returns the IDs of connectors the serving process can
	// construct right now. Liveness must come from the process that will run
	// the operation: a CLI's own registry sees the user's shell PATH and
	// credentials, which is not what the daemon has.
	ListLiveConnectors(ctx context.Context) ([]string, error)
	// ExecuteConnectorOperation runs a connector operation through the external connector service.
	ExecuteConnectorOperation(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error)
	// PluginHealth runs plugin install/load/health for a local plugin directory.
	PluginHealth(ctx context.Context, args PluginConnectorHealthArgs) (PluginConnectorHealth, error)
	// ExecutePluginConnector runs a connector operation through a local plugin directory.
	ExecutePluginConnector(ctx context.Context, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error)
	// InstallManagedPlugin validates and registers a plugin directory with the daemon manager.
	InstallManagedPlugin(ctx context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error)
	// LoadManagedPlugin starts a previously installed plugin by id.
	LoadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error)
	// UnloadManagedPlugin stops a loaded plugin by id.
	UnloadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error)
	// ListManagedPlugins returns currently installed daemon-managed plugins.
	ListManagedPlugins(ctx context.Context) ([]ManagedPluginConnectorState, error)
	// ManagedPluginHealth returns health for a daemon-managed plugin by id.
	ManagedPluginHealth(ctx context.Context, id string) (PluginConnectorHealth, error)
	// ExecuteManagedPlugin runs an operation through an already loaded daemon-managed plugin.
	ExecuteManagedPlugin(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error)
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

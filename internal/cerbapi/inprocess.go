package cerbapi

import (
	"context"
	"errors"
	"log/slog"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	contract "github.com/chrispian/cerberus/pkg/connector"
)

// InProcessClient satisfies Client by driving the daemon's shared runtime
// services directly. Used by the daemon process itself (for daemon-embedded
// MCP handlers) and by the socket server as its backend.
//
// The client is a thin adapter around the active v2 resource runtime.
type InProcessClient struct {
	local          *localconn.Connector
	logger         *slog.Logger
	runtime        *ResourceRuntimeService
	external       *ExternalConnectorService
	plugins        *PluginConnectorService
	managedPlugins *ManagedPluginConnectorService

	// Construction options forwarded to the shared runtime.
	cfgPath string
	cfg     *config.ConfigV2
}

// InProcessOption tunes construction of an InProcessClient.
type InProcessOption func(*InProcessClient)

// WithResourceRuntimeService injects the shared resource runtime layer used
// by resource-oriented client methods. Optional; when unset, the client
// constructs one from its config/local options.
func WithResourceRuntimeService(runtime *ResourceRuntimeService) InProcessOption {
	return func(c *InProcessClient) {
		if runtime != nil {
			c.runtime = runtime
		}
	}
}

// WithExternalConnectorService injects the external connector operation layer.
func WithExternalConnectorService(external *ExternalConnectorService) InProcessOption {
	return func(c *InProcessClient) {
		if external != nil {
			c.external = external
		}
	}
}

// WithPluginConnectorService injects the plugin-host-backed connector layer.
func WithPluginConnectorService(plugins *PluginConnectorService) InProcessOption {
	return func(c *InProcessClient) {
		if plugins != nil {
			c.plugins = plugins
		}
	}
}

func WithManagedPluginConnectorService(plugins *ManagedPluginConnectorService) InProcessOption {
	return func(c *InProcessClient) {
		if plugins != nil {
			c.managedPlugins = plugins
		}
	}
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
// config.yaml surface without a daemon restart.
//
// Production wiring: the daemon passes the same cfgPath that app.New
// used to bootstrap its initial Config.
func WithConfigPath(path string) InProcessOption {
	return func(c *InProcessClient) { c.cfgPath = path }
}

// WithLocalConnector attaches the shared local connector used by resource
// operations and pipeline execution. A default is constructed when omitted.
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

// NewInProcessClient constructs an InProcessClient.
func NewInProcessClient(opts ...InProcessOption) *InProcessClient {
	c := &InProcessClient{logger: slog.Default()}
	for _, opt := range opts {
		opt(c)
	}
	if c.runtime == nil {
		c.runtime = NewResourceRuntimeService(
			WithResourceRuntimeLogger(c.logger),
			WithResourceRuntimeLocalConnector(c.local),
			WithResourceRuntimeConfigV2(c.cfg),
			WithResourceRuntimeConfigPath(c.cfgPath),
		)
	}
	return c
}

// ListConnectors implements Client.
func (c *InProcessClient) ListConnectors(_ context.Context) ([]contract.Definition, error) {
	if c.external == nil {
		return nil, nil
	}
	return c.external.Definitions(), nil
}

// ListLiveConnectors implements Client.
func (c *InProcessClient) ListLiveConnectors(ctx context.Context) ([]string, error) {
	if c.external == nil {
		return nil, nil
	}
	defs := c.external.LiveDefinitionsContext(ctx)
	ids := make([]string, 0, len(defs))
	for _, def := range defs {
		ids = append(ids, def.ID)
	}
	return ids, nil
}

// ExecuteConnectorOperation implements Client.
func (c *InProcessClient) ExecuteConnectorOperation(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	if c.external == nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("external connector service is not configured"))
	}
	return c.external.Execute(ctx, args)
}

// PluginHealth implements Client.
func (c *InProcessClient) PluginHealth(ctx context.Context, args PluginConnectorHealthArgs) (PluginConnectorHealth, error) {
	if c.plugins == nil {
		return PluginConnectorHealth{}, errors.New("plugin connector service is not configured")
	}
	return c.plugins.Health(ctx, args)
}

// ExecutePluginConnector implements Client.
func (c *InProcessClient) ExecutePluginConnector(ctx context.Context, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	if c.plugins == nil {
		return ExternalConnectorOperationResult{}, errors.New("plugin connector service is not configured")
	}
	return c.plugins.Execute(ctx, args)
}

func (c *InProcessClient) InstallManagedPlugin(ctx context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error) {
	if c.managedPlugins == nil {
		return ManagedPluginConnectorState{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Install(ctx, args)
}

func (c *InProcessClient) LoadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	if c.managedPlugins == nil {
		return ManagedPluginConnectorState{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Load(ctx, id)
}

func (c *InProcessClient) UnloadManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	if c.managedPlugins == nil {
		return ManagedPluginConnectorState{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Unload(ctx, id)
}

func (c *InProcessClient) UninstallManagedPlugin(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	if c.managedPlugins == nil {
		return ManagedPluginConnectorState{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Uninstall(ctx, id)
}

func (c *InProcessClient) ListManagedPlugins(ctx context.Context) ([]ManagedPluginConnectorState, error) {
	if c.managedPlugins == nil {
		return nil, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.List(ctx)
}

func (c *InProcessClient) ManagedPluginHealth(ctx context.Context, id string) (PluginConnectorHealth, error) {
	if c.managedPlugins == nil {
		return PluginConnectorHealth{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Health(ctx, id)
}

func (c *InProcessClient) ExecuteManagedPlugin(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	if c.managedPlugins == nil {
		return ExternalConnectorOperationResult{}, errors.New("managed plugin connector service is not configured")
	}
	return c.managedPlugins.Execute(ctx, id, args)
}

// ResourceLogs implements Client.
func (c *InProcessClient) ResourceLogs(ctx context.Context, id string, lines int, stream string) (*LogLines, error) {
	return c.runtime.ResourceLogs(ctx, id, lines, stream)
}

// Health implements Client.
func (c *InProcessClient) Health(ctx context.Context, id string) (*DaemonHealth, error) {
	resourceEntries, err := c.runtime.Health(ctx, id)
	if err != nil {
		return nil, err
	}
	return &DaemonHealth{
		Resources:     resourceEntries,
		DaemonRunning: true,
	}, nil
}

// ListProjects implements Client.
//
// Delegates rather than assembling its own ProjectInfo: this used to be
// a second construction that could drift from the runtime service's
// view, and adding capabilities/links to only one of them is exactly
// how that drift starts.
func (c *InProcessClient) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	return c.runtime.ListProjects(ctx)
}

// ListResources implements Client.
func (c *InProcessClient) ListResources(ctx context.Context, args ResourceListArgs) ([]ResourceInfo, error) {
	return c.runtime.ListResources(ctx, args)
}

// ResolveDiagnostics implements Client.
func (c *InProcessClient) ResolveDiagnostics(ctx context.Context) (*ResolveDiagnostics, error) {
	return c.runtime.ResolveDiagnostics(ctx)
}

// GetResourceRuntime implements Client.
func (c *InProcessClient) GetResourceRuntime(ctx context.Context, id string) (*ResourceRuntimeStatus, error) {
	return c.runtime.GetResourceRuntime(ctx, id)
}

// GetResourceInspect implements Client.
func (c *InProcessClient) GetResourceInspect(ctx context.Context, id string) (*ResourceInspect, error) {
	return c.runtime.GetResourceInspect(ctx, id)
}

// GetResourceDoctor implements Client.
func (c *InProcessClient) GetResourceDoctor(ctx context.Context, id string) (*ResourceDoctor, error) {
	return c.runtime.GetResourceDoctor(ctx, id)
}

// DeployResource implements Client.
func (c *InProcessClient) DeployResource(ctx context.Context, id string, opts ...DeployResourceOption) (*OpResult, error) {
	return c.runtime.DeployResource(ctx, id, opts...)
}

func valueOrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// ReloadResource implements Client.
func (c *InProcessClient) ReloadResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.ReloadResource(ctx, id)
}

// StopResource implements Client.
func (c *InProcessClient) StopResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.StopResource(ctx, id)
}

// ApplyResource implements Client.
func (c *InProcessClient) ApplyResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.ApplyResource(ctx, id)
}

// SyncResource implements Client.
func (c *InProcessClient) SyncResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.SyncResource(ctx, id)
}

// RemoveResource implements Client.
func (c *InProcessClient) RemoveResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.RemoveResource(ctx, id)
}

// ListPipelines implements Client.
func (c *InProcessClient) ListPipelines(ctx context.Context) ([]PipelineInfo, error) {
	return c.runtime.ListPipelines(ctx)
}

// GetPipeline implements Client.
func (c *InProcessClient) GetPipeline(ctx context.Context, id string) (*PipelineDetail, error) {
	return c.runtime.GetPipeline(ctx, id)
}

// RunPipeline implements Client.
func (c *InProcessClient) RunPipeline(ctx context.Context, id string) (*PipelineRunResult, error) {
	return c.runtime.RunPipeline(ctx, id)
}

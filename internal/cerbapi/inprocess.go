package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline"
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

	// opMu serializes pipeline resolution/execution against the shared
	// local connector.
	opMu sync.Mutex
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

// ResourceLogs implements Client.
func (c *InProcessClient) ResourceLogs(_ context.Context, id string, lines int, stream string) (*LogLines, error) {
	return c.runtime.ResourceLogs(context.Background(), id, lines, stream)
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
	return c.runtime.ListResources(ctx, args)
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
func (c *InProcessClient) DeployResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.DeployResource(ctx, id)
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
func (c *InProcessClient) SyncResource(_ context.Context, id string) (*OpResult, error) {
	return c.runtime.SyncResource(context.Background(), id)
}

// RemoveResource implements Client.
func (c *InProcessClient) RemoveResource(ctx context.Context, id string) (*OpResult, error) {
	return c.runtime.RemoveResource(ctx, id)
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
	p, err := pipeline.Resolve(*pdef, c.pipelineResources(cfg), c.local)
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

func (c *InProcessClient) pipelineResources(cfg *config.ConfigV2) []config.ResourceDef {
	if cfg == nil {
		return nil
	}
	return append([]config.ResourceDef(nil), cfg.Resources...)
}

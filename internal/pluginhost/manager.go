package pluginhost

import (
	"context"
	"fmt"
	"sync"
)

// Installer is the install side of the plugin host. The initial manager keeps
// install separate from subprocess launch so we can wire catalog/extract/verify
// independently from plugin-sdk process transport.
type Installer interface {
	Install(ctx context.Context, source string) (InstalledPlugin, error)
}

// Process is the protocol-level view of a running plugin subprocess.
type Process interface {
	Init(ctx context.Context, params SDKInitParams) (SDKInitResult, error)
	Load(ctx context.Context) (SDKLoadResult, error)
	Unload(ctx context.Context) error
	Health(ctx context.Context) (SDKHealthResult, error)
	CallTool(ctx context.Context, req SDKMCPCallRequest) (SDKMCPCallResult, error)
	Close() error
}

// Launcher starts a plugin subprocess and returns a protocol client for it.
type Launcher interface {
	Launch(ctx context.Context, plugin InstalledPlugin) (Process, error)
}

type Manager struct {
	mu        sync.RWMutex
	installer Installer
	launcher  Launcher
	policy    TrustPolicy
	hostInfo  SDKHostInfo

	installed map[string]InstalledPlugin
	running   map[string]*loadedPlugin
}

type loadedPlugin struct {
	plugin  InstalledPlugin
	process Process
	init    SDKInitResult
	load    SDKLoadResult
}

func NewManager(installer Installer, launcher Launcher, policy TrustPolicy, hostVersion string) *Manager {
	return &Manager{
		installer: installer,
		launcher:  launcher,
		policy:    policy,
		hostInfo: SDKHostInfo{
			Version:  hostVersion,
			Protocol: SDKProtocolVersion,
		},
		installed: make(map[string]InstalledPlugin),
		running:   make(map[string]*loadedPlugin),
	}
}

var _ Host = (*Manager)(nil)

func (m *Manager) Install(ctx context.Context, source string) (InstalledPlugin, error) {
	if m.installer == nil {
		return InstalledPlugin{}, fmt.Errorf("plugin installer is not configured")
	}
	plugin, err := m.installer.Install(ctx, source)
	if err != nil {
		return InstalledPlugin{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.installed[plugin.ID] = plugin
	return plugin, nil
}

func (m *Manager) Load(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.running[id]; ok {
		return nil
	}
	plugin, ok := m.installed[id]
	if !ok {
		return fmt.Errorf("plugin %q is not installed", id)
	}
	if m.launcher == nil {
		return fmt.Errorf("plugin launcher is not configured")
	}

	process, err := m.launcher.Launch(ctx, plugin)
	if err != nil {
		return err
	}

	initResult, err := process.Init(ctx, SDKInitParams{
		PluginDir: plugin.Path,
		Config:    map[string]string{},
		LogLevel:  "info",
		HostInfo:  m.hostInfo,
	})
	if err != nil {
		_ = process.Close()
		return fmt.Errorf("plugin %q init: %w", id, err)
	}
	if initResult.Protocol != SDKProtocolVersion {
		_ = process.Close()
		return fmt.Errorf("plugin %q protocol %d does not match host protocol %d", id, initResult.Protocol, SDKProtocolVersion)
	}

	loadResult, err := process.Load(ctx)
	if err != nil {
		_ = process.Close()
		return fmt.Errorf("plugin %q load: %w", id, err)
	}

	m.running[id] = &loadedPlugin{
		plugin:  plugin,
		process: process,
		init:    initResult,
		load:    loadResult,
	}
	return nil
}

func (m *Manager) Unload(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	lp, ok := m.running[id]
	if !ok {
		return nil
	}
	delete(m.running, id)

	if err := lp.process.Unload(ctx); err != nil {
		_ = lp.process.Close()
		return fmt.Errorf("plugin %q unload: %w", id, err)
	}
	if err := lp.process.Close(); err != nil {
		return fmt.Errorf("plugin %q close: %w", id, err)
	}
	return nil
}

func (m *Manager) Health(ctx context.Context, id string) (Health, error) {
	m.mu.RLock()
	lp, running := m.running[id]
	m.mu.RUnlock()

	if !running {
		return Health{ID: id, Loaded: false, Healthy: false, Message: "not loaded"}, nil
	}
	result, err := lp.process.Health(ctx)
	if err != nil {
		return Health{}, fmt.Errorf("plugin %q health: %w", id, err)
	}
	return Health{
		ID:      id,
		Loaded:  true,
		Healthy: result.OK,
		Message: result.Message,
	}, nil
}

func (m *Manager) ExecuteOperation(ctx context.Context, args OperationArgs) (OperationResult, error) {
	m.mu.RLock()
	lp, ok := m.running[args.Connector]
	m.mu.RUnlock()
	if !ok {
		return OperationResult{}, fmt.Errorf("plugin %q is not loaded", args.Connector)
	}

	op, ok := OperationFromToolName(args.Connector, ToolNameForOperation(args.Connector, args.Operation), lp.plugin.Manifest)
	if !ok {
		return OperationResult{}, fmt.Errorf("plugin %q does not declare operation %q", args.Connector, args.Operation)
	}
	if err := OperationAllowed(lp.plugin.Trust.Tier, op, args.Acknowledged); err != nil {
		return OperationResult{}, err
	}

	result, err := lp.process.CallTool(ctx, MCPRequestFromOperation(args))
	if err != nil {
		return OperationResult{}, fmt.Errorf("plugin %q tool %q: %w", args.Connector, args.Operation, err)
	}
	return OperationResultFromMCP(args, result)
}

func (m *Manager) RegisterInstalled(plugin InstalledPlugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installed[plugin.ID] = plugin
}

// Remove drops an installed plugin from the registry. The caller unloads it
// first; a still-running plugin is refused rather than silently orphaned.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.running[id]; running {
		return fmt.Errorf("plugin %q is still loaded; unload it before removing", id)
	}
	if _, ok := m.installed[id]; !ok {
		return fmt.Errorf("plugin %q is not installed", id)
	}
	delete(m.installed, id)
	return nil
}

func (m *Manager) Installed(id string) (InstalledPlugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugin, ok := m.installed[id]
	return plugin, ok
}

func (m *Manager) InstalledPlugins() []InstalledPlugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]InstalledPlugin, 0, len(m.installed))
	for _, plugin := range m.installed {
		out = append(out, plugin)
	}
	return out
}

func (m *Manager) Loaded(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.running[id]
	return ok
}

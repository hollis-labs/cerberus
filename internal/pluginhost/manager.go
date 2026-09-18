package pluginhost

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/redact"
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
	secrets   SecretResolver
	warn      func(string)

	installed map[string]InstalledPlugin
	running   map[string]*loadedPlugin
}

type loadedPlugin struct {
	plugin  InstalledPlugin
	process Process
	init    SDKInitResult
	load    SDKLoadResult

	// missingSecrets names the required credentials that were absent when this
	// plugin was loaded. Names only — resolved values live in the subprocess.
	missingSecrets []string
}

// ManagerOption configures a Manager. The constructor stays positional for the
// four things a manager cannot work without; everything optional arrives here
// so adding a host capability does not touch every call site.
type ManagerOption func(*Manager)

// WithSecretResolver gives the manager the secret provider the built-in
// connectors resolve through. Without it a plugin is handed no credentials at
// all, which is what the host did before WP-7 and what the tests that do not
// care about secrets still get.
func WithSecretResolver(resolver SecretResolver) ManagerOption {
	return func(m *Manager) { m.secrets = resolver }
}

// WithLoadWarning receives one line per non-fatal problem found while loading a
// plugin — a declared credential that would not resolve, most of all. Lines
// carry secret names and redacted errors, never a value.
func WithLoadWarning(warn func(string)) ManagerOption {
	return func(m *Manager) { m.warn = warn }
}

func NewManager(installer Installer, launcher Launcher, policy TrustPolicy, hostVersion string, opts ...ManagerOption) *Manager {
	m := &Manager{
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
	for _, opt := range opts {
		opt(m)
	}
	return m
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

	// Decided once, here, and used for both the environment the subprocess is
	// launched with and the granted list it is told about. A plugin that
	// declared nothing gets nothing.
	plugin.Granted = GrantCapabilities(plugin.Spec.Capabilities)

	process, err := m.launcher.Launch(ctx, plugin)
	if err != nil {
		return err
	}

	// The plugin's declared credentials are resolved host-side and travel in
	// the Init config map. They deliberately do not travel in the environment:
	// a subprocess inherits ambient env, so an env-carried credential would
	// reach every plugin rather than the one that declared it.
	resolved := resolvePluginSecrets(ctx, m.secrets, plugin)
	m.reportSecretProblems(id, resolved)

	initResult, err := process.Init(ctx, SDKInitParams{
		PluginDir: plugin.Path,
		Config:    resolved.Config,
		LogLevel:  "info",
		HostInfo:  m.hostInfo,
		Granted:   plugin.Granted,
	})
	if err != nil {
		_ = process.Close()
		// Redacted, not wrapped: this error text originates in the plugin, and
		// the payload it just received carries credentials. A plugin that
		// echoes its init params into an error must not launder them into the
		// daemon log.
		return fmt.Errorf("plugin %q init: %s", id, redact.Text(err.Error()))
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
		plugin:         plugin,
		process:        process,
		init:           initResult,
		load:           loadResult,
		missingSecrets: resolved.MissingRequired,
	}
	return nil
}

// reportSecretProblems surfaces a credential that would not resolve without
// failing the load. Callers see it on stderr at load time and again, actionably,
// on the first operation that fails.
func (m *Manager) reportSecretProblems(id string, resolved resolvedSecrets) {
	if m.warn == nil {
		return
	}
	for _, problem := range resolved.Problems {
		m.warn(fmt.Sprintf("plugin %q credential lookup failed: %s", id, problem))
	}
	if len(resolved.MissingRequired) > 0 {
		m.warn(fmt.Sprintf(
			"plugin %q loaded without required credential %s; operations needing it will fail with credential_missing",
			id, strings.Join(resolved.MissingRequired, ", ")))
	}
}

// MissingSecrets names the required credentials a loaded plugin did not
// receive. Empty for a plugin that is not loaded.
func (m *Manager) MissingSecrets(id string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lp, ok := m.running[id]
	if !ok {
		return nil
	}
	return append([]string(nil), lp.missingSecrets...)
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
		// A plugin that loaded without a declared credential gets its failures
		// explained rather than pre-empted: operations that do not need the
		// secret keep working, and the one that 401s says which credential is
		// missing and how to supply it.
		if len(lp.missingSecrets) > 0 {
			return OperationResult{}, &MissingSecretsError{
				Connector: args.Connector,
				Secrets:   append([]string(nil), lp.missingSecrets...),
				Err:       err,
			}
		}
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

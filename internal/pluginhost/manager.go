package pluginhost

import (
	"context"
	"errors"
	"fmt"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
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
	hostInfo  SDKHostInfo
	secrets   SecretResolver
	settings  func() (ConnectorConfig, error)
	warn      func(string)

	// configProblems holds why each plugin's settings were last refused, so
	// a plugin that did not load can say why. Cleared by a successful load.
	configProblems map[string][]string

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

	// settings is what connector-config.yaml gave this plugin at load:
	// delivered field names, MCP exposure and the file's fingerprint.
	settings ResolvedSettings

	// redactor knows the credential values this host resolved for the plugin,
	// so it can remove them from any text the plugin sends back. The values
	// are fixed at load, so one redactor built then serves every request.
	redactor redact.Redactor
	// stderr correlates the plugin's stderr lines with the operations
	// running when it wrote them.
	stderr *stderrTap
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

// WithConnectorConfig gives the manager the loader for connector-config.yaml.
// It is called on every load, so a reload picks up an edited file. Without it
// a plugin gets no fields and exposes nothing.
func WithConnectorConfig(load func() (ConnectorConfig, error)) ManagerOption {
	return func(m *Manager) { m.settings = load }
}

// WithLoadWarning receives one line per non-fatal problem found while loading a
// plugin — a declared credential that would not resolve, most of all. Lines
// carry secret names and redacted errors, never a value.
func WithLoadWarning(warn func(string)) ManagerOption {
	return func(m *Manager) { m.warn = warn }
}

func NewManager(installer Installer, launcher Launcher, hostVersion string, opts ...ManagerOption) *Manager {
	m := &Manager{
		installer: installer,
		launcher:  launcher,
		hostInfo: SDKHostInfo{
			Version:  hostVersion,
			Protocol: SDKProtocolVersion,
		},
		installed:      make(map[string]InstalledPlugin),
		running:        make(map[string]*loadedPlugin),
		configProblems: make(map[string][]string),
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

	// The bundle is checked before anything else runs: a plugin that is not
	// the one the operator reviewed, or was built for another contract, is
	// refused before its code starts.
	if err := CheckBundle(plugin); err != nil {
		return err
	}

	// Settings are checked before the subprocess starts. A problem refuses
	// the load: a field meant to choose a target that was dropped would leave
	// the plugin acting on its default instead, which is the worse failure.
	settings, err := m.resolveSettings(plugin)
	if err != nil {
		return err
	}

	// Decided once, here, and used for both the environment the subprocess is
	// launched with and the granted list it is told about. A plugin that
	// declared nothing gets nothing.
	plugin.Granted = GrantCapabilities(plugin.Spec.Capabilities)

	tap := newStderrTap()
	process, err := m.launcher.Launch(withStderrTap(ctx, tap), plugin)
	if err != nil {
		return err
	}

	// The plugin's declared credentials are resolved host-side and travel in
	// the Init config map. They deliberately do not travel in the environment:
	// a subprocess inherits ambient env, so an env-carried credential would
	// reach every plugin rather than the one that declared it.
	resolved := resolvePluginSecrets(ctx, m.secrets, plugin)
	m.reportSecretProblems(id, resolved)
	redactor, unprotected := resolved.redactor()
	m.reportUnprotectedSecrets(id, unprotected)
	tap.setRedactor(redactor)

	// Fields travel beside the secrets. The manifest refuses a field and a
	// secret with the same name, so neither can shadow the other.
	initConfig := make(map[string]string, len(resolved.Config)+len(settings.Config))
	for name, value := range settings.Config {
		initConfig[name] = value
	}
	for name, value := range resolved.Config {
		initConfig[name] = value
	}

	initResult, err := process.Init(ctx, SDKInitParams{
		PluginDir: plugin.Path,
		Config:    initConfig,
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
		return fmt.Errorf("plugin %q init: %s", id, redactor.Text(err.Error()))
	}
	if initResult.Protocol != SDKProtocolVersion {
		_ = process.Close()
		return fmt.Errorf("plugin %q protocol %d does not match host protocol %d", id, initResult.Protocol, SDKProtocolVersion)
	}

	loadResult, err := process.Load(ctx)
	if err != nil {
		_ = process.Close()
		return fmt.Errorf("plugin %q load: %w", id, redactor.Error(err))
	}

	m.running[id] = &loadedPlugin{
		plugin:         plugin,
		process:        process,
		init:           initResult,
		load:           loadResult,
		missingSecrets: resolved.MissingRequired,
		settings:       settings,
		redactor:       redactor,
		stderr:         tap,
	}
	return nil
}

// resolveSettings reads connector-config.yaml for the plugin and checks it
// against the manifest. Called with m.mu held.
func (m *Manager) resolveSettings(plugin InstalledPlugin) (ResolvedSettings, error) {
	cfg := ConnectorConfig{Entries: map[string]PluginSettings{}}
	if m.settings != nil {
		loaded, err := m.settings()
		if err != nil {
			m.configProblems[plugin.ID] = []string{err.Error()}
			return ResolvedSettings{}, fmt.Errorf("plugin %q not loaded: %s is unreadable: %w", plugin.ID, ConnectorConfigFilename, err)
		}
		cfg = loaded
	}
	settings := cfg.ForPlugin(plugin)
	if m.warn != nil {
		for _, warning := range settings.Warnings {
			m.warn(warning)
		}
	}
	if len(settings.Problems) > 0 {
		m.configProblems[plugin.ID] = append([]string(nil), settings.Problems...)
		return ResolvedSettings{}, fmt.Errorf("plugin %q not loaded: its entry in %s is refused — %s",
			plugin.ID, ConnectorConfigFilename, strings.Join(settings.Problems, "; "))
	}
	delete(m.configProblems, plugin.ID)
	return settings, nil
}

// Settings reports what connector-config.yaml gave a loaded plugin. ok is
// false for a plugin that is not loaded.
func (m *Manager) Settings(id string) (ResolvedSettings, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lp, ok := m.running[id]
	if !ok {
		return ResolvedSettings{}, false
	}
	return lp.settings, true
}

// ConfigProblems reports why a plugin's settings were last refused. Empty
// once it loads.
func (m *Manager) ConfigProblems(id string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.configProblems[id]...)
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

// reportUnprotectedSecrets names the credentials whose values are too short
// for the plugin redactor to remove without eating ordinary text. Names only.
func (m *Manager) reportUnprotectedSecrets(id string, names []string) {
	if m.warn == nil || len(names) == 0 {
		return
	}
	m.warn(fmt.Sprintf(
		"plugin %q credential %s is shorter than %d bytes and is not redacted from the plugin's text",
		id, strings.Join(names, ", "), minRedactedValueLength))
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
		return fmt.Errorf("plugin %q unload: %w", id, lp.redactor.Error(err))
	}
	if err := lp.process.Close(); err != nil {
		return fmt.Errorf("plugin %q close: %w", id, lp.redactor.Error(err))
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
		return Health{}, fmt.Errorf("plugin %q health: %w", id, lp.redactor.Error(err))
	}
	return Health{
		ID:      id,
		Loaded:  true,
		Healthy: result.OK,
		Message: lp.redactor.Text(result.Message),
	}, nil
}

func (m *Manager) ExecuteOperation(ctx context.Context, args OperationArgs) (OperationResult, error) {
	m.mu.RLock()
	lp, ok := m.running[args.Connector]
	m.mu.RUnlock()
	if !ok {
		return OperationResult{}, fmt.Errorf("plugin %q %w", args.Connector, ErrNotLoaded)
	}

	op, ok := OperationFromToolName(args.Connector, ToolNameForOperation(args.Connector, args.Operation), lp.plugin.Manifest)
	if !ok {
		return OperationResult{}, fmt.Errorf("plugin %q: %w %q", args.Connector, ErrOperationUndeclared, args.Operation)
	}
	// The key table runs first, so a bad argument is refused without
	// calling the plugin, and before anything needs its credential.
	if err := op.Operation().CheckInputs(args.Config, true); err != nil {
		return OperationResult{}, fmt.Errorf("plugin %q operation %q: %w", args.Connector, args.Operation, err)
	}
	// A dry run whose preview the operator accepted at review stands in for
	// acknowledgment. The plugin is still told the caller's own flag.
	acknowledged := args.Acknowledged || (args.DryRun && PreviewAccepted(lp.plugin, op))
	if err := OperationAllowed(lp.plugin.Origin, op, acknowledged); err != nil {
		return OperationResult{}, err
	}
	// A dry run reaches the plugin only for an operation whose manifest
	// declares supports_dry, and the plugin is trusted to honor dry_run.
	// That preview is plugin-claimed, not verified by the host (Decision 7 in
	// docs/plans/live-systems-security-target.md); install review in P1 is
	// what turns the claim into something an operator accepted.
	if args.DryRun && op.EffectivePreview() == contract.PreviewNone {
		return OperationResult{}, fmt.Errorf("plugin %q operation %q: %w", args.Connector, args.Operation, ErrPreviewUnsupported)
	}

	// Everything the plugin sends back as text passes the value redactor
	// before it can reach an error, a log line or an MCP notification. The
	// plugin is expected to scrub its own credentials; this is the host's
	// backstop for one that does not (I10).
	// An audited call carries a collector: it gets the stderr lines written
	// while the call runs and the telemetry in the result, redacted.
	collector := collectorFrom(ctx)
	if collector != nil && lp.stderr != nil {
		defer lp.stderr.attach(collector)()
	}
	result, err := lp.process.CallTool(ctx, MCPRequestFromOperation(args))
	if err == nil {
		var events []pluginsdk.TelemetryEvent
		result.Content, events = pluginsdk.SplitTelemetry(result.Content)
		if collector != nil {
			collector.addEvents(events, lp.redactor)
		}
	}
	if err != nil {
		err = lp.redactor.Error(err)
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
	out, err := OperationResultFromMCP(args, result)
	if err != nil {
		var coded *CodedError
		if errors.As(err, &coded) {
			coded.MissingSecrets = append([]string(nil), lp.missingSecrets...)
		}
		return OperationResult{}, lp.redactor.Error(err)
	}
	return out, nil
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

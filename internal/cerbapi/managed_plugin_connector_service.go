package cerbapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

type ManagedPluginConnectorState struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Loaded  bool   `json:"loaded"`
	// Origin is how the plugin was installed: "installed", or "dev" for a
	// development install whose destructive operations are refused. It is not
	// a trust level.
	Origin string `json:"origin,omitempty"`
	// EntrypointSHA256 fingerprints the entrypoint as installed. Change
	// detection for later, not a trust signal; nothing compares it yet (P1).
	EntrypointSHA256 string `json:"entrypoint_sha256,omitempty"`

	// MissingSecrets names required credentials a loaded plugin did not
	// receive. Reported so `managed list` is truthful about a plugin that is
	// loaded but cannot authenticate, rather than leaving that to be
	// discovered by the first failing operation. Names only.
	MissingSecrets []string `json:"missing_secrets,omitempty"`

	// Capabilities names the ambient host access this plugin declared, and
	// Granted what it actually holds. Reported so an operator can answer "what
	// can this plugin reach" by reading a list rather than by reading the
	// launch code. Names only, like MissingSecrets.
	Capabilities []string `json:"capabilities,omitempty"`
	Granted      []string `json:"granted,omitempty"`

	// ContractGaps lists what the plugin's manifest leaves undeclared and how
	// the host reads each gap — an operation with no effect is treated as
	// exec. A gap is reported, not refused, so existing plugins keep loading.
	// Always present, as [] when there are none, so "no gaps" is something a
	// reader can see rather than infer from a missing field.
	ContractGaps []string `json:"contract_gaps"`

	// ConfigFields names the connector-config.yaml fields a loaded plugin
	// received, MCPExpose the operations whose MCP tools it serves, and
	// ConfigSHA256 the file version it read. Names and a fingerprint only;
	// values are the operator's to read in the file. Always present, as []
	// when empty, so "nothing exposed" is visible rather than inferred.
	ConfigFields []string `json:"config_fields"`
	MCPExpose    []string `json:"mcp_expose"`
	ConfigSHA256 string   `json:"config_sha256,omitempty"`
	// ConfigProblems is why the plugin's settings were refused, which is why
	// it is not loaded. Always present, as [] when there are none.
	ConfigProblems []string `json:"config_problems"`
}

type ManagedPluginConnectorService struct {
	manager     *pluginhost.Manager
	hostVersion string
	statePath   string
	records     map[string]pluginConnectorPersistedEntry
	warn        io.Writer
	reservedIDs []string

	// unrestored holds entries whose plugin directory could not be restored at
	// startup. They are kept so persist() does not silently drop a registration
	// whose directory is temporarily absent — a rebuilt plugin comes back on the
	// next restart instead of having to be reinstalled.
	unrestored []pluginConnectorPersistedEntry

	audit  audit.Sink
	logger *slog.Logger
}

// ManagedPluginOption configures optional managed-plugin host capabilities.
type ManagedPluginOption func(*managedPluginConfig)

type managedPluginConfig struct {
	secrets     pluginhost.SecretResolver
	reservedIDs []string
	configPath  string
}

// WithManagedPluginConnectorConfig names connector-config.yaml, the
// operator-owned file of plugin fields and MCP exposure. It is read on every
// load, so `managed load` after an edit picks the change up.
func WithManagedPluginConnectorConfig(path string) ManagedPluginOption {
	return func(c *managedPluginConfig) { c.configPath = path }
}

// WithManagedPluginSecrets hands the managed plugin host the same secret
// provider the built-in connectors resolve through, so a plugin's declared
// credentials come from `connector-secrets.yaml` and `keychain://` exactly as
// docs/secrets.md describes. Without it plugins load with no credentials.
func WithManagedPluginSecrets(resolver pluginhost.SecretResolver) ManagedPluginOption {
	return func(c *managedPluginConfig) { c.secrets = resolver }
}

// WithManagedPluginReservedIDs names the connector ids the host serves itself.
// A plugin claiming one is refused at install rather than being allowed into
// the inventory to shadow a built-in. Pass Registry.BuiltInIDs().
//
// Only the managed lane takes this. `connectors plugin exec` installs into a
// throwaway host for one call and registers nothing, so it cannot shadow
// anything — and refusing there would break the docker prototype that exists to
// demonstrate plugin authoring against a built-in's shape.
func WithManagedPluginReservedIDs(ids ...string) ManagedPluginOption {
	return func(c *managedPluginConfig) { c.reservedIDs = append(c.reservedIDs, ids...) }
}

// NewManagedPluginConnectorService constructs the managed plugin lane. The
// audit sink is required, as for the admin lane: plugin operations on the
// direct route, and install, load, unload and uninstall, write their records
// to it.
func NewManagedPluginConnectorService(sink audit.Sink, hostVersion string, stderr io.Writer, statePath string, opts ...ManagedPluginOption) (*ManagedPluginConnectorService, error) {
	if sink == nil {
		panic("cerbapi: NewManagedPluginConnectorService requires an audit sink")
	}
	var cfg managedPluginConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	service := &ManagedPluginConnectorService{
		hostVersion: hostVersion,
		statePath:   statePath,
		records:     make(map[string]pluginConnectorPersistedEntry),
		warn:        stderr,
		reservedIDs: cfg.reservedIDs,
		audit:       sink,
		logger:      slog.Default(),
	}
	service.manager = pluginhost.NewManager(
		nil,
		pluginhost.SubprocessLauncher{
			Transport: pluginhost.StdioTransportFactory{Stderr: stderr},
			Env:       pluginLaunchEnv(),
		},
		hostVersion,
		pluginhost.WithSecretResolver(cfg.secrets),
		pluginhost.WithConnectorConfig(connectorConfigLoader(cfg.configPath)),
		pluginhost.WithLoadWarning(func(line string) { service.warnf("%s", line) }),
	)
	if err := restoreManagedPlugins(context.Background(), service, statePath); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *ManagedPluginConnectorService) Install(ctx context.Context, args PluginConnectorHealthArgs) (_ ManagedPluginConnectorState, retErr error) {
	call, err := s.beginAdmin(ctx, "install", map[string]any{"plugin_dir": args.PluginDir})
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	defer func() { call.finish(retErr) }()

	progressToken := fmt.Sprintf("managed-plugin-install:%s", args.PluginDir)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Installing managed plugin from %s", args.PluginDir))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Installing managed plugin")
	installed, err := s.install(args.PluginDir, args.InstallOptions())
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin install failed for %s: %s", args.PluginDir, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin install failed")
		return ManagedPluginConnectorState{}, err
	}
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin install failed for %s: %s", installed.ID, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin install failed")
		return ManagedPluginConnectorState{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin installed: %s", installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin installed")
	return state, nil
}

func (s *ManagedPluginConnectorService) Load(ctx context.Context, id string) (_ ManagedPluginConnectorState, retErr error) {
	call, err := s.beginAdmin(ctx, "load", map[string]any{"id": id})
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	defer func() { call.finish(retErr) }()

	progressToken := fmt.Sprintf("managed-plugin-load:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Loading managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Loading managed plugin")
	if err := s.manager.Load(context.Background(), id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin load failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin load failed")
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := s.state(installed, true)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin load failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin load failed")
		return ManagedPluginConnectorState{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin loaded: %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin loaded")
	return state, nil
}

func (s *ManagedPluginConnectorService) Unload(ctx context.Context, id string) (_ ManagedPluginConnectorState, retErr error) {
	call, err := s.beginAdmin(ctx, "unload", map[string]any{"id": id})
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	defer func() { call.finish(retErr) }()

	progressToken := fmt.Sprintf("managed-plugin-unload:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Unloading managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Unloading managed plugin")
	if err := s.manager.Unload(ctx, id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin unload failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin unload failed")
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin unload failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin unload failed")
		return ManagedPluginConnectorState{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin unloaded: %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin unloaded")
	return state, nil
}

// Uninstall unloads the plugin if needed and drops it from the managed set and
// the persisted state. Without it the only way to undo an install was editing
// ~/.cerberus/plugin-connectors.json by hand.
func (s *ManagedPluginConnectorService) Uninstall(ctx context.Context, id string) (_ ManagedPluginConnectorState, retErr error) {
	call, err := s.beginAdmin(ctx, "uninstall", map[string]any{"id": id})
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	defer func() { call.finish(retErr) }()

	progressToken := fmt.Sprintf("managed-plugin-uninstall:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Uninstalling managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Uninstalling managed plugin")

	installed, ok := s.manager.Installed(id)
	if !ok {
		err := fmt.Errorf("plugin %q is not installed", id)
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
		return ManagedPluginConnectorState{}, err
	}
	if s.manager.Loaded(id) {
		if err := s.manager.Unload(ctx, id); err != nil {
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, redact.Text(err.Error())))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
			return ManagedPluginConnectorState{}, err
		}
	}
	if err := s.manager.Remove(id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
		return ManagedPluginConnectorState{}, err
	}
	delete(s.records, id)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
		return ManagedPluginConnectorState{}, err
	}

	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin uninstalled: %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstalled")
	return managedState(installed, false), nil
}

func (s *ManagedPluginConnectorService) List(context.Context) ([]ManagedPluginConnectorState, error) {
	plugins := s.manager.InstalledPlugins()
	out := make([]ManagedPluginConnectorState, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, s.state(plugin, s.manager.Loaded(plugin.ID)))
	}
	return out, nil
}

func (s *ManagedPluginConnectorService) Definitions() []contract.Definition {
	plugins := s.manager.InstalledPlugins()
	out := make([]contract.Definition, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, contract.DefinitionFromManifest(plugin.Manifest))
	}
	return out
}

func (s *ManagedPluginConnectorService) LiveDefinitions() []contract.Definition {
	plugins := s.manager.InstalledPlugins()
	out := make([]contract.Definition, 0, len(plugins))
	for _, plugin := range plugins {
		if s.manager.Loaded(plugin.ID) {
			out = append(out, contract.DefinitionFromManifest(plugin.Manifest))
		}
	}
	return out
}

func (s *ManagedPluginConnectorService) Installed(id string) bool {
	_, ok := s.manager.Installed(id)
	return ok
}

func (s *ManagedPluginConnectorService) Loaded(id string) bool {
	return s.manager.Loaded(id)
}

func (s *ManagedPluginConnectorService) Health(ctx context.Context, id string) (PluginConnectorHealth, error) {
	health, err := s.manager.Health(ctx, id)
	if err != nil {
		return PluginConnectorHealth{}, err
	}
	return PluginConnectorHealth{
		ID:      health.ID,
		Loaded:  health.Loaded,
		Healthy: health.Healthy,
		Message: health.Message,
	}, nil
}

// Execute runs a plugin operation on the direct plugin route, recording it
// the way the admin lane records its operations.
func (s *ManagedPluginConnectorService) Execute(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	spec := auditSpec{connector: id, operation: args.Operation, config: args.Config, acknowledged: args.Acknowledged, dryRun: args.DryRun}
	spec.pluginConfigSHA256, spec.pluginEntrypointSHA256 = s.fingerprints(id)
	for _, def := range s.Definitions() {
		if def.ID == id {
			spec.op, spec.known = def.Operation(args.Operation)
			spec.credentials = credentialNames(def)
		}
	}
	call, err := beginAudit(ctx, s.audit, s.logger, spec)
	if err != nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: id, Operation: args.Operation}, ExternalConnectorAuditUnavailable, err)
	}
	result, err := s.execute(ctx, id, args)
	call.finish(err)
	return result, err
}

// execute runs a plugin operation unrecorded: for a caller that has written
// the records itself, the admin lane.
func (s *ManagedPluginConnectorService) execute(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	progressToken := fmt.Sprintf("managed-plugin-exec:%s:%s", id, args.Operation)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing managed plugin operation %s on %s", args.Operation, id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Executing managed plugin operation")
	result, err := s.manager.ExecuteOperation(ctx, pluginhost.OperationArgs{
		Connector:    id,
		Operation:    args.Operation,
		Config:       args.Config,
		DryRun:       args.DryRun,
		Acknowledged: args.Acknowledged,
	})
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin operation %s failed on %s: %s", args.Operation, id, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin operation failed")
		// Coded here, so the direct plugin route answers a refusal with the
		// same code and status as the admin lane does.
		return ExternalConnectorOperationResult{}, managedPluginExecuteError(ExternalConnectorOperationArgs{Connector: id, Operation: args.Operation}, err)
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin operation %s completed on %s", args.Operation, id))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin operation completed")
	return ExternalConnectorOperationResult{
		Connector: result.Connector,
		Operation: result.Operation,
		Data:      result.Data,
	}, nil
}

func managedState(plugin pluginhost.InstalledPlugin, loaded bool) ManagedPluginConnectorState {
	declared := make([]string, 0, len(plugin.Spec.Capabilities))
	for _, req := range plugin.Spec.Capabilities {
		declared = append(declared, req.Name)
	}
	// Both lists are reported, not just the grant: "asked for and did not get"
	// is the interesting case, and a single list cannot say it.
	return ManagedPluginConnectorState{
		ID:               plugin.ID,
		Version:          plugin.Version,
		Path:             plugin.Path,
		Loaded:           loaded,
		Origin:           string(plugin.Origin),
		EntrypointSHA256: plugin.EntrypointSHA256,
		Capabilities:     declared,
		ContractGaps:     plugin.Manifest.ContractGaps(),
		// The grant is decided at load. For a plugin that is installed but not
		// loaded, plugin.Granted is empty, so compute what it would receive —
		// otherwise `managed list` shows a plugin asking for access and
		// apparently holding none, which reads as a refusal rather than as
		// "not started yet".
		Granted: grantedOrPreview(plugin),
	}
}

// grantedOrPreview reports the recorded grant, or what the grant would be for a
// plugin that has not been loaded yet.
func grantedOrPreview(plugin pluginhost.InstalledPlugin) []string {
	if len(plugin.Granted) > 0 {
		return plugin.Granted
	}
	return pluginhost.GrantCapabilities(plugin.Spec.Capabilities)
}

func (s *ManagedPluginConnectorService) state(plugin pluginhost.InstalledPlugin, loaded bool) ManagedPluginConnectorState {
	out := managedState(plugin, loaded)
	if loaded {
		out.MissingSecrets = s.manager.MissingSecrets(plugin.ID)
		if settings, ok := s.manager.Settings(plugin.ID); ok {
			out.ConfigFields = settings.Fields
			out.MCPExpose = settings.Expose
			out.ConfigSHA256 = settings.SHA256
		}
	}
	out.ConfigProblems = s.manager.ConfigProblems(plugin.ID)
	if out.ConfigFields == nil {
		out.ConfigFields = []string{}
	}
	if out.MCPExpose == nil {
		out.MCPExpose = []string{}
	}
	if out.ConfigProblems == nil {
		out.ConfigProblems = []string{}
	}
	return out
}

// connectorConfigLoader reads connector-config.yaml afresh on each call. No
// path means no file: no fields, nothing exposed.
func connectorConfigLoader(path string) func() (pluginhost.ConnectorConfig, error) {
	return func() (pluginhost.ConnectorConfig, error) {
		return pluginhost.LoadConnectorConfig(path)
	}
}

func (s *ManagedPluginConnectorService) install(pluginDir string, options PluginInstallOptions) (pluginhost.InstalledPlugin, error) {
	installer := pluginhost.DirectoryInstaller{
		Policy:      pluginPolicy(pluginDir, options),
		ReservedIDs: s.reservedIDs,
	}
	installed, err := installer.Install(context.Background(), pluginDir)
	if err != nil {
		return pluginhost.InstalledPlugin{}, err
	}
	s.manager.RegisterInstalled(installed)
	s.records[installed.ID] = pluginConnectorPersistedEntry{
		PluginDir: pluginDir,
		Options:   options,
		Loaded:    s.manager.Loaded(installed.ID),
	}
	return installed, nil
}

func (s *ManagedPluginConnectorService) persist() error {
	if s.statePath == "" {
		return nil
	}
	plugins := s.manager.InstalledPlugins()
	state := pluginConnectorPersistedState{
		Entries: make([]pluginConnectorPersistedEntry, 0, len(plugins)),
	}
	for _, plugin := range plugins {
		record := s.records[plugin.ID]
		record.Loaded = s.manager.Loaded(plugin.ID)
		record.PluginDir = plugin.Path
		s.records[plugin.ID] = record
		state.Entries = append(state.Entries, pluginConnectorPersistedEntry{
			PluginDir: record.PluginDir,
			Options:   record.Options,
			Loaded:    record.Loaded,
		})
	}
	state.Entries = append(state.Entries, s.unrestored...)
	return writePluginConnectorState(s.statePath, state)
}

// warnf reports a non-fatal managed-plugin problem. Startup problems must be
// visible without being fatal, so they go to the daemon's stderr log.
func (s *ManagedPluginConnectorService) warnf(format string, args ...any) {
	if s == nil || s.warn == nil {
		return
	}
	fmt.Fprintf(s.warn, "cerberus: managed plugin: "+format+"\n", args...)
}

// pluginAdminOperation is the contract the audit log records plugin
// lifecycle calls under. It is admin: it changes what Cerberus can run. It is
// not a declared, gated operation yet — P1-5 makes install an admin
// operation with a review and a TTY confirmation — so it lives here, for the
// record, rather than in a Definition that would claim a gate nothing enforces.
func pluginAdminOperation(name string) contract.Operation {
	return contract.Operation{
		Name: name, Effect: contract.EffectAdmin,
		Target:  contract.TargetDescriptor{Kind: "plugin", From: []string{"id", "plugin_dir"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
	}.Finalize()
}

// beginAdmin writes the intent record of a plugin lifecycle call. Admin is not
// a read, so an unwritable log refuses the call.
func (s *ManagedPluginConnectorService) beginAdmin(ctx context.Context, operation string, target map[string]any) (*auditCall, error) {
	spec := auditSpec{connector: "plugin", operation: operation, op: pluginAdminOperation(operation), known: true, config: target}
	call, err := beginAudit(ctx, s.audit, s.logger, spec)
	if err != nil {
		return nil, externalConnectorError(ExternalConnectorOperationArgs{Connector: "plugin", Operation: operation}, ExternalConnectorAuditUnavailable, err)
	}
	return call, nil
}

// fingerprints are a loaded plugin's config and entrypoint hashes, for its
// audit records.
func (s *ManagedPluginConnectorService) fingerprints(id string) (config, entrypoint string) {
	if s == nil || !s.manager.Loaded(id) {
		return "", ""
	}
	if settings, ok := s.manager.Settings(id); ok {
		config = settings.SHA256
	}
	if installed, ok := s.manager.Installed(id); ok {
		entrypoint = installed.EntrypointSHA256
	}
	return config, entrypoint
}

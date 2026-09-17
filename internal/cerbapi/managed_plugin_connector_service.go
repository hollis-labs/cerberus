package cerbapi

import (
	"context"
	"fmt"
	"io"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

type ManagedPluginConnectorState struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Loaded    bool   `json:"loaded"`
	TrustTier string `json:"trust_tier,omitempty"`

	// MissingSecrets names required credentials a loaded plugin did not
	// receive. Reported so `managed list` is truthful about a plugin that is
	// loaded but cannot authenticate, rather than leaving that to be
	// discovered by the first failing operation. Names only.
	MissingSecrets []string `json:"missing_secrets,omitempty"`
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
}

// ManagedPluginOption configures optional managed-plugin host capabilities.
type ManagedPluginOption func(*managedPluginConfig)

type managedPluginConfig struct {
	secrets     pluginhost.SecretResolver
	reservedIDs []string
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

func NewManagedPluginConnectorService(hostVersion string, stderr io.Writer, statePath string, opts ...ManagedPluginOption) (*ManagedPluginConnectorService, error) {
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
	}
	service.manager = pluginhost.NewManager(
		nil,
		pluginhost.SubprocessLauncher{
			Transport: pluginhost.StdioTransportFactory{Stderr: stderr},
			Env:       pluginLaunchEnv(),
		},
		pluginhost.DefaultTrustPolicy(),
		hostVersion,
		pluginhost.WithSecretResolver(cfg.secrets),
		pluginhost.WithLoadWarning(func(line string) { service.warnf("%s", line) }),
	)
	if err := restoreManagedPlugins(context.Background(), service, statePath); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *ManagedPluginConnectorService) Install(ctx context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error) {
	progressToken := fmt.Sprintf("managed-plugin-install:%s", args.PluginDir)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Installing managed plugin from %s", args.PluginDir))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Installing managed plugin")
	installed, err := s.install(args.PluginDir, args.Trust)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin install failed for %s: %s", args.PluginDir, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin install failed")
		return ManagedPluginConnectorState{}, err
	}
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin install failed for %s: %s", installed.ID, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin install failed")
		return ManagedPluginConnectorState{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin installed: %s", installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin installed")
	return state, nil
}

func (s *ManagedPluginConnectorService) Load(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	progressToken := fmt.Sprintf("managed-plugin-load:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Loading managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Loading managed plugin")
	if err := s.manager.Load(context.Background(), id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin load failed for %s: %s", id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin load failed")
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := s.state(installed, true)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin load failed for %s: %s", id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin load failed")
		return ManagedPluginConnectorState{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Managed plugin loaded: %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin loaded")
	return state, nil
}

func (s *ManagedPluginConnectorService) Unload(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	progressToken := fmt.Sprintf("managed-plugin-unload:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Unloading managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Unloading managed plugin")
	if err := s.manager.Unload(ctx, id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin unload failed for %s: %s", id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin unload failed")
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin unload failed for %s: %s", id, err.Error()))
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
func (s *ManagedPluginConnectorService) Uninstall(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	progressToken := fmt.Sprintf("managed-plugin-uninstall:%s", id)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Uninstalling managed plugin %s", id))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Uninstalling managed plugin")

	installed, ok := s.manager.Installed(id)
	if !ok {
		err := fmt.Errorf("plugin %q is not installed", id)
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
		return ManagedPluginConnectorState{}, err
	}
	if s.manager.Loaded(id) {
		if err := s.manager.Unload(ctx, id); err != nil {
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, err.Error()))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
			return ManagedPluginConnectorState{}, err
		}
	}
	if err := s.manager.Remove(id); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin uninstall failed")
		return ManagedPluginConnectorState{}, err
	}
	delete(s.records, id)
	if err := s.persist(); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin uninstall failed for %s: %s", id, err.Error()))
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

func (s *ManagedPluginConnectorService) Execute(ctx context.Context, id string, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
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
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Managed plugin operation %s failed on %s: %s", args.Operation, id, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Managed plugin operation failed")
		return ExternalConnectorOperationResult{}, err
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
	return ManagedPluginConnectorState{
		ID:        plugin.ID,
		Version:   plugin.Version,
		Path:      plugin.Path,
		Loaded:    loaded,
		TrustTier: string(plugin.Trust.Tier),
	}
}

func (s *ManagedPluginConnectorService) state(plugin pluginhost.InstalledPlugin, loaded bool) ManagedPluginConnectorState {
	out := managedState(plugin, loaded)
	if loaded {
		out.MissingSecrets = s.manager.MissingSecrets(plugin.ID)
	}
	return out
}

func (s *ManagedPluginConnectorService) install(pluginDir string, trust PluginConnectorTrustOptions) (pluginhost.InstalledPlugin, error) {
	installer := pluginhost.DirectoryInstaller{
		Policy:        pluginPolicy(pluginDir, trust),
		RequestedTier: requestedPluginTier(trust),
		CatalogSigned: trust.CatalogSigned,
		ArchiveSHA256: trust.ArchiveSHA256,
		ArchiveSigned: trust.ArchiveSigned,
		ReservedIDs:   s.reservedIDs,
	}
	installed, err := installer.Install(context.Background(), pluginDir)
	if err != nil {
		return pluginhost.InstalledPlugin{}, err
	}
	s.manager.RegisterInstalled(installed)
	s.records[installed.ID] = pluginConnectorPersistedEntry{
		PluginDir: pluginDir,
		Trust:     trust,
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
			Trust:     record.Trust,
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

package cerbapi

import (
	"context"
	"io"

	"github.com/chrispian/cerberus/internal/pluginhost"
	contract "github.com/chrispian/cerberus/pkg/connector"
)

type ManagedPluginConnectorState struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Loaded    bool   `json:"loaded"`
	TrustTier string `json:"trust_tier,omitempty"`
}

type ManagedPluginConnectorService struct {
	manager     *pluginhost.Manager
	hostVersion string
	statePath   string
	records     map[string]pluginConnectorPersistedEntry
}

func NewManagedPluginConnectorService(hostVersion string, stderr io.Writer, statePath string) (*ManagedPluginConnectorService, error) {
	service := &ManagedPluginConnectorService{
		manager: pluginhost.NewManager(
			nil,
			pluginhost.SubprocessLauncher{
				Transport: pluginhost.StdioTransportFactory{Stderr: stderr},
				Env:       pluginLaunchEnv(),
			},
			pluginhost.DefaultTrustPolicy(),
			hostVersion,
		),
		hostVersion: hostVersion,
		statePath:   statePath,
		records:     make(map[string]pluginConnectorPersistedEntry),
	}
	if err := restoreManagedPlugins(context.Background(), service, statePath); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *ManagedPluginConnectorService) Install(ctx context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error) {
	installed, err := s.install(args.PluginDir, args.Trust)
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return state, nil
}

func (s *ManagedPluginConnectorService) Load(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	if err := s.manager.Load(context.Background(), id); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := managedState(installed, true)
	if err := s.persist(); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return state, nil
}

func (s *ManagedPluginConnectorService) Unload(ctx context.Context, id string) (ManagedPluginConnectorState, error) {
	if err := s.manager.Unload(ctx, id); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	installed, _ := s.manager.Installed(id)
	state := managedState(installed, false)
	if err := s.persist(); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return state, nil
}

func (s *ManagedPluginConnectorService) List(context.Context) ([]ManagedPluginConnectorState, error) {
	plugins := s.manager.InstalledPlugins()
	out := make([]ManagedPluginConnectorState, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, managedState(plugin, s.manager.Loaded(plugin.ID)))
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
	result, err := s.manager.ExecuteOperation(ctx, pluginhost.OperationArgs{
		Connector:    id,
		Operation:    args.Operation,
		Config:       args.Config,
		DryRun:       args.DryRun,
		Acknowledged: args.Acknowledged,
	})
	if err != nil {
		return ExternalConnectorOperationResult{}, err
	}
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

func (s *ManagedPluginConnectorService) install(pluginDir string, trust PluginConnectorTrustOptions) (pluginhost.InstalledPlugin, error) {
	installer := pluginhost.DirectoryInstaller{
		Policy:        pluginPolicy(pluginDir, trust),
		RequestedTier: requestedPluginTier(trust),
		CatalogSigned: trust.CatalogSigned,
		ArchiveSHA256: trust.ArchiveSHA256,
		ArchiveSigned: trust.ArchiveSigned,
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
	return writePluginConnectorState(s.statePath, state)
}

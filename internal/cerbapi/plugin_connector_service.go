package cerbapi

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/chrispian/cerberus/internal/pluginhost"
)

type PluginConnectorTrustOptions struct {
	DevMode       bool   `json:"dev_mode,omitempty"`
	CatalogSigned bool   `json:"catalog_signed,omitempty"`
	ArchiveSigned bool   `json:"archive_signed,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
}

type PluginConnectorHealthArgs struct {
	PluginDir string                      `json:"plugin_dir"`
	Trust     PluginConnectorTrustOptions `json:"trust"`
}

type PluginConnectorExecArgs struct {
	PluginDir    string                      `json:"plugin_dir"`
	Operation    string                      `json:"operation"`
	Config       map[string]any              `json:"config,omitempty"`
	DryRun       bool                        `json:"dry_run,omitempty"`
	Acknowledged bool                        `json:"acknowledged,omitempty"`
	Trust        PluginConnectorTrustOptions `json:"trust"`
}

type PluginConnectorHealth struct {
	ID      string `json:"id"`
	Loaded  bool   `json:"loaded"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

type PluginConnectorService struct {
	hostVersion string
	stderr      io.Writer
}

func NewPluginConnectorService(hostVersion string, stderr io.Writer) *PluginConnectorService {
	return &PluginConnectorService{
		hostVersion: hostVersion,
		stderr:      stderr,
	}
}

func (s *PluginConnectorService) Health(ctx context.Context, args PluginConnectorHealthArgs) (PluginConnectorHealth, error) {
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.Trust)
	if err != nil {
		return PluginConnectorHealth{}, err
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()
	health, err := manager.Health(ctx, installed.ID)
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

func (s *PluginConnectorService) Execute(ctx context.Context, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.Trust)
	if err != nil {
		return ExternalConnectorOperationResult{}, err
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()
	result, err := manager.ExecuteOperation(ctx, pluginhost.OperationArgs{
		Connector:    installed.ID,
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

func (s *PluginConnectorService) installAndLoad(ctx context.Context, pluginDir string, trust PluginConnectorTrustOptions) (*pluginhost.Manager, pluginhost.InstalledPlugin, error) {
	installer := pluginhost.DirectoryInstaller{
		Policy:        pluginPolicy(pluginDir, trust),
		RequestedTier: requestedPluginTier(trust),
		CatalogSigned: trust.CatalogSigned,
		ArchiveSHA256: trust.ArchiveSHA256,
		ArchiveSigned: trust.ArchiveSigned,
	}

	manager := pluginhost.NewManager(
		installer,
		pluginhost.SubprocessLauncher{
			Transport: pluginhost.StdioTransportFactory{Stderr: s.stderr},
			Env:       pluginLaunchEnv(),
		},
		installer.Policy,
		s.hostVersion,
	)

	installed, err := manager.Install(ctx, pluginDir)
	if err != nil {
		return nil, pluginhost.InstalledPlugin{}, err
	}
	if err := manager.Load(ctx, installed.ID); err != nil {
		return nil, pluginhost.InstalledPlugin{}, err
	}
	return manager, installed, nil
}

func pluginPolicy(pluginDir string, trust PluginConnectorTrustOptions) pluginhost.TrustPolicy {
	if trust.DevMode {
		return pluginhost.DeveloperTrustPolicy(pluginDir)
	}
	return pluginhost.DefaultTrustPolicy()
}

func requestedPluginTier(trust PluginConnectorTrustOptions) pluginhost.TrustTier {
	if trust.DevMode {
		return pluginhost.TrustTierUnsignedDev
	}
	return pluginhost.TrustTierSigned
}

func pluginLaunchEnv() []string {
	allowed := []string{
		"PATH",
		"HOME",
		"TMPDIR",
		"TMP",
		"TEMP",
		"USER",
		"LOGNAME",
		"LANG",
		"LC_ALL",
		"DOCKER_HOST",
		"DOCKER_CONTEXT",
		"DOCKER_CONFIG",
		"XDG_RUNTIME_DIR",
		"SSH_AUTH_SOCK",
	}
	envMap := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		envMap[key] = true
	}

	out := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if envMap[key] || strings.HasPrefix(key, "GO_WANT_") {
			out = append(out, entry)
		}
	}
	return out
}

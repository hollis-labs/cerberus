package cerbapi

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	gmcp "github.com/hollis-labs/go-mcp/server"
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
	secrets     pluginhost.SecretResolver
}

// PluginConnectorOption configures the one-shot plugin host used by
// `cerberus connectors plugin health|exec`.
type PluginConnectorOption func(*PluginConnectorService)

// WithPluginConnectorSecrets hands the one-shot plugin host the secret
// provider built-in connectors resolve through, so an ad-hoc `plugin exec`
// sees the same credentials the daemon-managed lane does.
func WithPluginConnectorSecrets(resolver pluginhost.SecretResolver) PluginConnectorOption {
	return func(s *PluginConnectorService) { s.secrets = resolver }
}

func NewPluginConnectorService(hostVersion string, stderr io.Writer, opts ...PluginConnectorOption) *PluginConnectorService {
	service := &PluginConnectorService{
		hostVersion: hostVersion,
		stderr:      stderr,
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func (s *PluginConnectorService) Health(ctx context.Context, args PluginConnectorHealthArgs) (PluginConnectorHealth, error) {
	progressToken := fmt.Sprintf("plugin-health:%s", args.PluginDir)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Starting plugin health check for %s", args.PluginDir))
	gmcp.NotifyProgress(ctx, progressToken, 0, 3, "Installing plugin")
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.Trust)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin health check failed for %s: %s", args.PluginDir, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin health failed")
		return PluginConnectorHealth{}, err
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Checking plugin health for %s", installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 2, 3, "Checking plugin health")
	health, err := manager.Health(ctx, installed.ID)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin health check failed for %s: %s", installed.ID, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin health failed")
		return PluginConnectorHealth{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Plugin health check completed for %s", installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin health completed")
	return PluginConnectorHealth{
		ID:      health.ID,
		Loaded:  health.Loaded,
		Healthy: health.Healthy,
		Message: health.Message,
	}, nil
}

func (s *PluginConnectorService) Execute(ctx context.Context, args PluginConnectorExecArgs) (ExternalConnectorOperationResult, error) {
	progressToken := fmt.Sprintf("plugin-exec:%s:%s", args.PluginDir, args.Operation)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Starting plugin operation %s for %s", args.Operation, args.PluginDir))
	gmcp.NotifyProgress(ctx, progressToken, 0, 3, "Installing plugin")
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.Trust)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin operation %s failed for %s: %s", args.Operation, args.PluginDir, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin operation failed")
		return ExternalConnectorOperationResult{}, err
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing plugin operation %s on %s", args.Operation, installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 2, 3, "Executing plugin operation")
	result, err := manager.ExecuteOperation(ctx, pluginhost.OperationArgs{
		Connector:    installed.ID,
		Operation:    args.Operation,
		Config:       args.Config,
		DryRun:       args.DryRun,
		Acknowledged: args.Acknowledged,
	})
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin operation %s failed on %s: %s", args.Operation, installed.ID, err.Error()))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin operation failed")
		return ExternalConnectorOperationResult{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Plugin operation %s completed on %s", args.Operation, installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin operation completed")
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
		pluginhost.WithSecretResolver(s.secrets),
		pluginhost.WithLoadWarning(s.warn),
	)

	installed, err := manager.Install(ctx, pluginDir)
	if err != nil {
		return nil, pluginhost.InstalledPlugin{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Loaded plugin manifest for %s", installed.ID))
	if err := manager.Load(ctx, installed.ID); err != nil {
		return nil, pluginhost.InstalledPlugin{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Plugin %s loaded", installed.ID))
	return manager, installed, nil
}

// pluginPolicy picks the trust policy for an install. Signature claims opt into
// the signed path; everything else installs as an unsigned local plugin, which
// is the supported default rather than an escape hatch. DevMode remains for the
// stricter developer-roots policy in a devmode build.
func pluginPolicy(pluginDir string, trust PluginConnectorTrustOptions) pluginhost.TrustPolicy {
	if trust.DevMode {
		return pluginhost.DeveloperTrustPolicy(pluginDir)
	}
	if trust.CatalogSigned || trust.ArchiveSigned {
		return pluginhost.DefaultTrustPolicy()
	}
	return pluginhost.LocalTrustPolicy()
}

func requestedPluginTier(trust PluginConnectorTrustOptions) pluginhost.TrustTier {
	if trust.DevMode {
		return pluginhost.TrustTierUnsignedDev
	}
	if trust.CatalogSigned || trust.ArchiveSigned {
		return pluginhost.TrustTierSigned
	}
	return pluginhost.TrustTierUnsigned
}

// warn reports a non-fatal plugin-host problem to the caller's stderr. Silence
// is the fallback, never a panic: `plugin exec` runs with a nil writer from the
// CLI.
func (s *PluginConnectorService) warn(line string) {
	if s == nil || s.stderr == nil {
		return
	}
	fmt.Fprintf(s.stderr, "cerberus: plugin: %s\n", line)
}

// pluginLaunchEnv is the allow-list a plugin subprocess inherits. It carries no
// credentials by design: env is ambient and would reach every plugin, so a
// plugin's declared secrets travel in the Init config channel instead. Do not
// widen this with credential entries.
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

package cerbapi

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

// Refusals for a filesystem path arriving where only an installed plugin's id
// is accepted. Running a directory executes its entrypoint, so it is offered
// only in-process, from the operator's own shell.
const (
	PluginDirRetired     = "running a plugin directory is not available over the socket or the web console; run `cerberus connectors plugin health <dir>` or `cerberus connectors plugin exec <dir> <operation>` in your shell, or address an installed plugin by id"
	PluginDirNotAccepted = "plugin_dir is not accepted here: address the installed plugin by id; to run a directory, use `cerberus connectors plugin exec <dir> <operation>` in your shell"
)

// PluginInstallOptions are how a plugin is installed. There is no trust or
// signing option: Cerberus does not vet plugins, so nothing a caller could
// assert about a plugin changes what it may do. DevMode selects a development
// install, which is a restriction (see pluginhost.OriginDev).
type PluginInstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

// legacyInstallOptions reads the "trust" object older CLIs sent and older
// state files stored. Only dev_mode still means anything; catalog_signed,
// archive_signed and archive_sha256 were self-asserted and are ignored.
type legacyInstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

func mergeLegacyOptions(options PluginInstallOptions, legacy *legacyInstallOptions) PluginInstallOptions {
	if legacy != nil && legacy.DevMode {
		options.DevMode = true
	}
	return options
}

type PluginConnectorHealthArgs struct {
	PluginDir string               `json:"plugin_dir"`
	Options   PluginInstallOptions `json:"options"`
	// LegacyTrust is the pre-P0-4 name for Options, read so an older CLI's
	// --dev is not silently dropped. It is never written.
	LegacyTrust *legacyInstallOptions `json:"trust,omitempty"`
}

// InstallOptions returns the options, folding in the legacy field.
func (a PluginConnectorHealthArgs) InstallOptions() PluginInstallOptions {
	return mergeLegacyOptions(a.Options, a.LegacyTrust)
}

type PluginConnectorExecArgs struct {
	PluginDir    string               `json:"plugin_dir"`
	Operation    string               `json:"operation"`
	Config       map[string]any       `json:"config,omitempty"`
	DryRun       bool                 `json:"dry_run,omitempty"`
	Acknowledged bool                 `json:"acknowledged,omitempty"`
	Options      PluginInstallOptions `json:"options"`
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
	configPath  string
}

// PluginConnectorOption configures the one-shot plugin host used by
// `cerberus connectors plugin health|exec`.
type PluginConnectorOption func(*PluginConnectorService)

// WithPluginConnectorConfig names connector-config.yaml for the one-shot
// host, so an ad-hoc `plugin exec` sees the same fields the managed lane does.
func WithPluginConnectorConfig(path string) PluginConnectorOption {
	return func(s *PluginConnectorService) { s.configPath = path }
}

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
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.InstallOptions())
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin health check failed for %s: %s", args.PluginDir, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin health failed")
		return PluginConnectorHealth{}, err
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Checking plugin health for %s", installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 2, 3, "Checking plugin health")
	health, err := manager.Health(ctx, installed.ID)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin health check failed for %s: %s", installed.ID, redact.Text(err.Error())))
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
	manager, installed, err := s.installAndLoad(ctx, args.PluginDir, args.Options)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin operation %s failed for %s: %s", args.Operation, args.PluginDir, redact.Text(err.Error())))
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
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Plugin operation %s failed on %s: %s", args.Operation, installed.ID, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin operation failed")
		// Coded the way the admin lane codes it, so the one-shot `plugin exec`
		// reports the same code a managed plugin would.
		return ExternalConnectorOperationResult{}, managedPluginExecuteError(ExternalConnectorOperationArgs{Connector: installed.ID, Operation: args.Operation}, err)
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Plugin operation %s completed on %s", args.Operation, installed.ID))
	gmcp.NotifyProgress(ctx, progressToken, 3, 3, "Plugin operation completed")
	return ExternalConnectorOperationResult{
		Connector: result.Connector,
		Operation: result.Operation,
		Data:      result.Data,
	}, nil
}

func (s *PluginConnectorService) installAndLoad(ctx context.Context, pluginDir string, options PluginInstallOptions) (*pluginhost.Manager, pluginhost.InstalledPlugin, error) {
	installer := pluginhost.DirectoryInstaller{Policy: pluginPolicy(pluginDir, options)}

	manager := pluginhost.NewManager(
		installer,
		pluginhost.SubprocessLauncher{
			Transport: pluginhost.StdioTransportFactory{Stderr: s.stderr},
			Env:       pluginLaunchEnv(),
		},
		s.hostVersion,
		pluginhost.WithSecretResolver(s.secrets),
		pluginhost.WithConnectorConfig(connectorConfigLoader(s.configPath)),
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

// pluginPolicy picks the install policy: a development install when DevMode
// is set, a local install otherwise.
func pluginPolicy(pluginDir string, options PluginInstallOptions) pluginhost.InstallPolicy {
	if options.DevMode {
		return pluginhost.DeveloperInstallPolicy(pluginDir)
	}
	return pluginhost.LocalInstallPolicy()
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

// pluginLaunchEnv is the base allow-list every plugin subprocess inherits. It
// carries no credential and no credential *handle*: env is ambient and would
// reach every plugin, so a plugin's declared secrets travel in the Init config
// channel instead.
//
// Do not widen this with a credential entry, and note that "credential" here
// includes a handle that is not credential-shaped. SSH_AUTH_SOCK and the
// DOCKER_* variables used to live in this list, which meant every loaded plugin
// could authenticate as the operator to any host trusting their key, and reach
// any configured Docker daemon, whether or not it had asked for anything
// (CERB-GAP-837). They are now unlocked by a declared capability instead — see
// internal/pluginhost/capability.go. Anything with that character belongs
// there, not here.
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
		"XDG_RUNTIME_DIR",
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

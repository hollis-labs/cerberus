package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// ProcessBackendsOptions build the secret backends of a process that is not the server.
type ProcessBackendsOptions struct {
	// Gate records each backend load (audit only: a load here is never refused by policy).
	// Required: a backend is not started unrecorded.
	Gate *gate.Gate
	// Paths are the adopted root (plugin-connectors.json, plugins/, plugin-data/) and the config
	// dir (connector-secrets.yaml, a backend's own credential mapping).
	Paths Paths
	// ConnectorConfig is cerberus.connectors, the plugins' settings.
	ConnectorConfig pluginhost.ConnectorConfig
	// HostVersion is this build's version, for plugin host ranges.
	HostVersion string
	// Shim is the cerberus binary whose __plugin-exec sets a plugin's rlimits; empty launches
	// plugins directly.
	Shim string
	// Stderr receives plugin stderr and warnings.
	Stderr io.Writer
	Logger *slog.Logger
	// Keyring is the OS credential store a backend's own credential may come from; nil is the
	// Cerberus keychain.
	Keyring secret.ReadWriter
}

// ProcessBackends resolves vault references in a process that is not the server: `cerberus
// run-secrets`, which resolves a managed service's environment in the service's own process
// before it execs (the old build's cerbapi.ProcessSecretBackends).
//
// It reads plugin-connectors.json and checks each bundle as the admin lane does, but registers
// the plugins without loading any, loads only the backend a reference needs, once, and stops it
// at Close, before the process execs. It never writes the state file. It does not ask the
// server: a socket call that returned credential values would be an oracle for every same-user
// process, and a service must start while the server is down.
type ProcessBackends struct {
	p      *Plugins
	mu     sync.Mutex
	loaded []string
}

var _ secretref.SchemeRouter = (*ProcessBackends)(nil)

// NewProcessBackends registers the plugins in the adopted root's state file, loading none. A
// backend's own credential resolves through the core chain only (the environment,
// connector-secrets.yaml, the keyring), never another vault. No built-in connector is built and
// nothing is restored: only what a secret backend needs.
func NewProcessBackends(o ProcessBackendsOptions) (*ProcessBackends, error) {
	if o.Gate == nil {
		return nil, errors.New("admin: a secret backend's load is recorded, so the operation gate is required")
	}
	if o.Paths.AdoptRoot == "" || !filepath.IsAbs(o.Paths.AdoptRoot) {
		return nil, errors.New("admin: the adopted root must be an absolute path")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Keyring == nil {
		o.Keyring = secrets.NewKeychainProvider()
	}
	s := &Service{
		o: Options{Gate: o.Gate, Paths: o.Paths, ConnectorConfig: o.ConnectorConfig, HostVersion: o.HostVersion,
			Shim: o.Shim, Stderr: o.Stderr, Logger: o.Logger, Keyring: o.Keyring},
		gate: o.Gate, keyring: o.Keyring, backends: &secrets.BackendRouter{}, logger: o.Logger,
	}
	s.refs = secrets.NewReferenceProvider(o.Keyring, o.Paths.ConnectorSecrets(), secretref.WithSchemeRouter(s.backends))
	s.chain = secrets.Registering(s.refs, notACredential)
	s.core = secrets.Registering(secrets.NewReferenceProvider(o.Keyring, o.Paths.ConnectorSecrets(), secretref.WithoutSchemeRouter(coreSecretsOnly)), notACredential)
	// The built-in ids are reserved, so a plugin cannot register under one; their connectors
	// are not built here.
	s.registry = connector.NewRegistry()
	s.registry.RegisterDefinition(dockerconn.Definition())
	s.registry.RegisterDefinition(sshconn.Definition())
	p, err := newPlugins(s)
	if err != nil {
		return nil, err
	}
	return &ProcessBackends{p: p}, nil
}

// Claims reports whether an installed plugin claims scheme.
func (b *ProcessBackends) Claims(scheme string) bool { return b.p.Claims(scheme) }

// Backend names the plugin that resolves scheme, as id@version.
func (b *ProcessBackends) Backend(scheme string) string { return b.p.Backend(scheme) }

// ResolveSecret loads the backend that claims ref's scheme, if it is not loaded yet, and
// resolves ref through it. A backend whose install review is pending is refused: it would be
// handed secrets from code nobody accepted.
func (b *ProcessBackends) ResolveSecret(ctx context.Context, ref string) (string, error) {
	scheme, _, _ := strings.Cut(ref, "://")
	if id := b.p.manager.SchemeClaimant(scheme); id != "" {
		if err := b.load(ctx, id); err != nil {
			return "", &pluginhost.SecretBackendError{Scheme: scheme, Plugin: id, Reason: err.Error()}
		}
	}
	return b.p.ResolveSecret(ctx, ref)
}

func (b *ProcessBackends) load(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.p.manager.Loaded(id) {
		return nil
	}
	installed, ok := b.p.manager.Installed(id)
	if !ok {
		return errors.New("the plugin is not installed")
	}
	if installed.ReviewPending || installed.BundleDigest == "" {
		return fmt.Errorf("its install review is pending, and a managed service will not hand secrets to unreviewed code; run `cerberus connectors plugin managed review %s` in a terminal", id)
	}
	// Recorded like a restore: Cerberus starting plugin code on its own, never refused.
	call, _ := b.p.gate.BeginAudit(ctx, oneShotBackendSpec(installed))
	err := b.p.manager.Load(ctx, id)
	call.Finish(loadError(err))
	if err != nil {
		return err
	}
	b.loaded = append(b.loaded, id)
	return nil
}

// Close stops every backend this process loaded. run-secrets calls it before exec, so no plugin
// process outlives the resolution or leaks into the service.
func (b *ProcessBackends) Close(ctx context.Context) {
	b.mu.Lock()
	loaded := b.loaded
	b.loaded = nil
	b.mu.Unlock()
	for _, id := range loaded {
		_ = b.p.manager.Unload(ctx, id)
	}
}

// Loaded reports whether id's process is running in this process.
func (b *ProcessBackends) Loaded(id string) bool { return b.p.manager.Loaded(id) }

// oneShotBackendSpec is the record of a backend loaded by run-secrets: automation via
// run_secrets (the gate names the run_secrets surface), with the bundle its review accepted.
func oneShotBackendSpec(p pluginhost.InstalledPlugin) gate.Spec {
	return gate.Spec{Connector: "plugin", Operation: "load", Op: pluginAdminOperation("load"), Known: true,
		Config: map[string]any{"id": p.ID, "plugin_dir": p.Path}, Automation: true, AutomationVia: "run_secrets",
		Reason:                 "secret backend loaded by run-secrets to resolve a managed service's environment; bundle " + p.BundleDigest + " checked against its accepted review",
		PluginEntrypointSHA256: p.EntrypointSHA256}
}

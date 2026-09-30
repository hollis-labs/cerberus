package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/secretref"
)

// ProcessSecretBackends resolves vault references in a process that is not
// the daemon: `cerberus run-secrets`, which resolves a managed service's
// environment in the service's own process before it execs.
//
// It reads the same plugin state file the daemon does and checks each bundle
// the same way, but loads only the backend a reference needs, once, and
// stops it before the process execs. It never writes the state file. It does
// not ask the daemon: a socket call that returns credential values would be
// an oracle for every same-user process, and a service must still start
// while the daemon is down.
type ProcessSecretBackends struct {
	svc    *ManagedPluginConnectorService
	mu     sync.Mutex
	loaded []string
}

var _ secretref.SchemeRouter = (*ProcessSecretBackends)(nil)

// NewProcessSecretBackends registers the plugins in the state file at
// statePath, loading none. core resolves a backend's own credential: the
// core chain, never another vault.
func NewProcessSecretBackends(sink audit.Sink, hostVersion string, stderr io.Writer, statePath string, core pluginhost.SecretResolver, opts ...ManagedPluginOption) (*ProcessSecretBackends, error) {
	opts = append(opts, WithManagedPluginCoreSecrets(core), func(c *managedPluginConfig) { c.registerOnly = true })
	svc, err := NewManagedPluginConnectorService(sink, hostVersion, stderr, statePath, opts...)
	if err != nil {
		return nil, err
	}
	return &ProcessSecretBackends{svc: svc}, nil
}

// Claims reports whether an installed plugin claims scheme.
func (b *ProcessSecretBackends) Claims(scheme string) bool { return b.svc.Claims(scheme) }

// Backend names the plugin that resolves scheme, as id@version.
func (b *ProcessSecretBackends) Backend(scheme string) string { return b.svc.Backend(scheme) }

// ResolveSecret loads the backend that claims ref's scheme, if it is not
// loaded yet, and resolves ref through it. A backend whose install review is
// pending is refused: it would be handed secrets from code nobody accepted.
func (b *ProcessSecretBackends) ResolveSecret(ctx context.Context, ref string) (string, error) {
	scheme, _, _ := strings.Cut(ref, "://")
	if id := b.svc.manager.SchemeClaimant(scheme); id != "" {
		if err := b.load(ctx, id); err != nil {
			return "", &pluginhost.SecretBackendError{Scheme: scheme, Plugin: id, Reason: err.Error()}
		}
	}
	return b.svc.ResolveSecret(ctx, ref)
}

func (b *ProcessSecretBackends) load(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.svc.manager.Loaded(id) {
		return nil
	}
	installed, ok := b.svc.manager.Installed(id)
	if !ok {
		return errors.New("the plugin is not installed")
	}
	if installed.ReviewPending || installed.BundleDigest == "" {
		return fmt.Errorf("its install review is pending, and a managed service will not hand secrets to unreviewed code; run `cerberus connectors plugin managed review %s` in a terminal", id)
	}
	// Recorded like a restore: Cerberus starting plugin code on its own.
	call, _ := beginAudit(ctx, b.svc.audit, b.svc.logger, oneShotBackendSpec(installed))
	err := b.svc.manager.Load(ctx, id)
	call.finish(managedLoadError(err))
	if err != nil {
		return err
	}
	b.loaded = append(b.loaded, id)
	return nil
}

// Close stops every backend this process loaded. run-secrets calls it before
// exec, so no plugin process outlives the resolution or leaks into the
// service.
func (b *ProcessSecretBackends) Close(ctx context.Context) {
	b.mu.Lock()
	loaded := b.loaded
	b.loaded = nil
	b.mu.Unlock()
	for _, id := range loaded {
		_ = b.svc.manager.Unload(ctx, id)
	}
}

func oneShotBackendSpec(p pluginhost.InstalledPlugin) auditSpec {
	return auditSpec{connector: "plugin", operation: "load", op: pluginAdminOperation("load"), known: true,
		config: map[string]any{"id": p.ID, "plugin_dir": p.Path}, automation: true, automationVia: "run_secrets",
		reason:                 "secret backend loaded by run-secrets to resolve a managed service's environment; bundle " + p.BundleDigest + " checked against its accepted review",
		pluginEntrypointSHA256: p.EntrypointSHA256}
}

package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// A secret backend is a plugin that claims a reference scheme
// (plugin.SecretBackend): the host routes <scheme>:// references to it and
// it answers with the value, over command/execute, never over a tool.

// WithCoreSecretResolver sets the resolver a secret backend's own declared
// credentials come from. It is the core chain (the environment, the
// reference mapping and the OS credential store) with no plugin scheme in
// it, so a backend's credential never comes from another vault. Without it,
// a backend's credentials resolve like any plugin's.
func WithCoreSecretResolver(resolver SecretResolver) ManagerOption {
	return func(m *Manager) { m.coreSecrets = resolver }
}

// secretsFor is the resolver a plugin's declared credentials come from. A
// secret backend with no core resolver configured gets none that could reach
// a vault: it fails closed rather than falling back to the full chain, where
// a backend could depend on another, or on itself.
func (m *Manager) secretsFor(p InstalledPlugin) SecretResolver {
	if p.Spec.Cerberus.SecretBackend == nil {
		return m.secrets
	}
	if m.coreSecrets != nil {
		return m.coreSecrets
	}
	return noCoreResolver{}
}

// noCoreResolver is the core chain when none was configured: it resolves
// nothing, and says why.
type noCoreResolver struct{}

func (noCoreResolver) Get(context.Context, string, string) (string, error) {
	return "", errors.New("credential_missing: this host has no core credential chain configured for secret backends, so a backend's own credential cannot be resolved; a backend's credential must come from the OS credential store or the environment")
}

// SecretBackendError is a reference a secret backend could not resolve. It
// is always credential_missing: the credential an operation needed is not
// available, and there is no fallback to another.
type SecretBackendError struct {
	Scheme string
	// Plugin is the claimant, when one is installed.
	Plugin string
	// Reason is the recovery, composed by the host, or the backend's own
	// redacted explanation.
	Reason string
}

func (e *SecretBackendError) Error() string {
	if e.Plugin == "" {
		return "credential_missing: " + e.Reason
	}
	// The plugin id is not followed by a colon: an id such as onepassword
	// reads to redact.Text as a credential assignment, which would eat the
	// word after it.
	return fmt.Sprintf("credential_missing: %s:// resolves through plugin %s, and %s", e.Scheme, e.Plugin, e.Reason)
}

// ErrorCode is the error's code on every surface.
func (e *SecretBackendError) ErrorCode() string { return string(plugin.ErrorCredentialMissing) }

// secretBackendWait bounds how long a resolve waits for a backend that is
// still loading, inside the caller's own deadline. It is the host's default
// load deadline: a backend slower than that has failed its load anyway.
var secretBackendWait = DefaultLimits.Load

// SchemeClaimant is the installed plugin that claims scheme, or "".
func (m *Manager) SchemeClaimant(scheme string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.claimantLocked(scheme)
}

func (m *Manager) claimantLocked(scheme string) string {
	for id, p := range m.installed {
		if b := p.Spec.Cerberus.SecretBackend; b != nil && b.Scheme == scheme {
			return id
		}
	}
	return ""
}

// ClaimedSchemes lists the schemes installed plugins claim, by plugin id.
func (m *Manager) ClaimedSchemes() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]string{}
	for id, p := range m.installed {
		if b := p.Spec.Cerberus.SecretBackend; b != nil {
			out[b.Scheme] = id
		}
	}
	return out
}

// ResolveSecret resolves ref through the loaded plugin that claims its
// scheme. It waits, within ctx and secretBackendWait, for a backend that is
// still loading. Every failure is a *SecretBackendError naming the recovery;
// there is no fallback.
//
// A value it returns joins the backend's own redactor for the rest of the
// load, so the backend's later stderr, errors and status lose it. Registering
// it with the request's scope is the caller's (secrets.Registering), as for
// every other credential.
func (m *Manager) ResolveSecret(ctx context.Context, ref string) (string, error) {
	scheme, _, ok := strings.Cut(ref, "://")
	if !ok || scheme == "" {
		return "", &SecretBackendError{Reason: "the value is not a <scheme>:// reference"}
	}
	id := m.SchemeClaimant(scheme)
	if id == "" {
		return "", &SecretBackendError{Scheme: scheme, Reason: fmt.Sprintf(
			"no installed plugin resolves %s:// references; install and load the plugin that provides the %s scheme with `cerberus connectors plugin managed install <dir>`",
			scheme, scheme)}
	}
	lp, err := m.awaitBackend(ctx, id)
	if err != nil {
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: err.Error()}
	}
	cmd, ok := lp.process.(commander)
	if !ok {
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: "its transport cannot carry a resolve"}
	}
	args, err := json.Marshal(plugin.ResolveArgs{Ref: ref})
	if err != nil {
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: "the reference could not be encoded"}
	}
	var res SDKCommandResult
	err = m.supervisedCall(ctx, id, lp,
		rpcCall{Phase: "call", Name: "resolve", Timeout: lp.limits.ResolveTimeout(), Effect: contract.EffectReadSensitive},
		func(callCtx context.Context) error {
			var callErr error
			res, callErr = cmd.Command(callCtx, SDKCommandRequest{Name: plugin.ResolveCommand, Args: string(args)})
			return callErr
		})
	r := lp.currentRedactor()
	if err != nil {
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: r.Text(err.Error())}
	}
	switch res.Action {
	case plugin.ResolveActionValue:
	case plugin.ResolveActionError:
		reason := "the backend reported a failure it did not explain"
		if _, msg, coded := plugin.ResolveFailure(res.Content); coded && msg != "" {
			reason = msg
		}
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: r.Text(reason)}
	default:
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: fmt.Sprintf("the backend answered with action %q, which is not a resolve result", res.Action)}
	}
	if res.Content == "" {
		return "", &SecretBackendError{Scheme: scheme, Plugin: id, Reason: "the backend resolved the reference to an empty value"}
	}
	lp.hold(res.Content)
	return res.Content, nil
}

// awaitBackend is the running backend id, waiting for one still loading.
func (m *Manager) awaitBackend(ctx context.Context, id string) (*loadedPlugin, error) {
	m.mu.RLock()
	lp, running := m.running[id]
	wait, loading := m.loading[id]
	m.mu.RUnlock()
	if running {
		return lp, nil
	}
	if !loading {
		if reason := m.GaveUp(id); reason != "" {
			return nil, fmt.Errorf("the plugin stopped and Cerberus gave up restarting it (%s); load it again with `cerberus connectors plugin managed load %s`", reason, id)
		}
		if m.restarting(id) {
			return nil, errors.New("the plugin stopped and Cerberus is restarting it; retry shortly")
		}
		return nil, fmt.Errorf("the plugin is installed but not loaded; load it with `cerberus connectors plugin managed load %s`", id)
	}
	timer := time.NewTimer(secretBackendWait)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
		return nil, fmt.Errorf("the plugin is still loading after %s; retry shortly, or check `cerberus connectors plugin managed list`", secretBackendWait)
	case <-ctx.Done():
		return nil, fmt.Errorf("the plugin was still loading when this request's deadline passed; retry shortly")
	}
	m.mu.RLock()
	lp, running = m.running[id]
	loadErr := m.loadErr[id]
	m.mu.RUnlock()
	if running {
		return lp, nil
	}
	if loadErr != nil {
		return nil, fmt.Errorf("the plugin failed to load: %s", redact.Text(loadErr.Error()))
	}
	return nil, errors.New("the plugin is not loaded")
}

// currentRedactor is the plugin's redactor as it stands.
func (lp *loadedPlugin) currentRedactor() redact.Redactor {
	lp.redactMu.RLock()
	defer lp.redactMu.RUnlock()
	return lp.redactor
}

// hold adds a value a backend resolved to the plugin's redactor, in every
// form redact.Forms gives it, and to its stderr tap's.
func (lp *loadedPlugin) hold(value string) {
	forms := redact.Forms(value)
	if len(forms) == 0 {
		return
	}
	lp.redactMu.Lock()
	lp.redactor = lp.redactor.With(forms...)
	r := lp.redactor
	lp.redactMu.Unlock()
	if lp.stderr != nil {
		lp.stderr.setRedactor(r)
	}
}

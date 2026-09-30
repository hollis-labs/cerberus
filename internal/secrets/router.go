package secrets

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/secretref"
)

// BackendRouter routes vault references to the plugin host's secret
// backends. It exists before the host does: the credential chain is built
// when the process starts, and the host that serves backends is built later
// from it, then bound here. Until then, and in a process with no host, a
// vault reference fails as credential_missing naming the daemon.
type BackendRouter struct {
	mu     sync.RWMutex
	target secretref.SchemeRouter
}

var _ secretref.SchemeRouter = (*BackendRouter)(nil)

// Bind makes target the router's destination.
func (r *BackendRouter) Bind(target secretref.SchemeRouter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target = target
}

func (r *BackendRouter) bound() secretref.SchemeRouter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.target
}

// Claims reports whether the bound host claims scheme.
func (r *BackendRouter) Claims(scheme string) bool {
	if t := r.bound(); t != nil {
		return t.Claims(scheme)
	}
	return false
}

// ResolveSecret resolves ref through the bound host.
func (r *BackendRouter) ResolveSecret(ctx context.Context, ref string) (string, error) {
	t := r.bound()
	if t == nil {
		scheme, _, _ := strings.Cut(ref, "://")
		return "", fmt.Errorf("credential_missing: %s:// reference: %w: resolving it needs the Cerberus daemon, where secret-backend plugins run; start it with `cerberus daemon start` and run this through it",
			scheme, secretref.ErrNoSecretBackend)
	}
	return t.ResolveSecret(ctx, ref)
}

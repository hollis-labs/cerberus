package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hollis-labs/substrate/apierr"
)

// RefusedKeyScope reports why a scope may not be granted to an API key, a pairing code or any
// other machine credential, or "" when it may. Approving is the owner's console session alone
// (decision 0.1), so no machine credential may hold cerberus.approve, directly or through a
// wildcard that covers it ("*", "cerberus.*").
func RefusedKeyScope(scope string) string {
	switch strings.TrimSpace(scope) {
	case "*", "cerberus.*":
		return fmt.Sprintf("scope %q would cover %s, which no key may hold; grant cerberus.read, cerberus.operate or cerberus.admin", scope, ScopeApprove)
	case ScopeApprove:
		return fmt.Sprintf("%s is held only by the owner's console session, never by a key", ScopeApprove)
	}
	return ""
}

// mintRoutes are the identity routes that create a machine credential with scopes.
var mintRoutes = map[string]bool{"/api/identity/keys": true, "/api/identity/pairing-codes": true}

// KeyGuard refuses a request that would mint a machine credential holding a refused scope
// (RefusedKeyScope). It wraps the handler the identity routes are served from.
func KeyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !mintRoutes[strings.TrimSuffix(r.URL.Path, "/")] {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "invalid_args", "the request body could not be read", nil)
			return
		}
		var in struct {
			Scopes []string `json:"scopes"`
		}
		if json.Unmarshal(body, &in) == nil {
			for _, s := range in.Scopes {
				if why := RefusedKeyScope(s); why != "" {
					apierr.Write(w, http.StatusForbidden, "scope_not_grantable", why, map[string]any{"scope": s})
					return
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

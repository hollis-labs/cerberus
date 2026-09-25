// Package sentinel is the WP-S2 acceptance fixture: a credential that
// matches no redaction rule, and a real connector that fails by echoing it
// back through a vendor SDK. It is imported only by tests.
//
// The acceptance criterion it serves: a credential resolved during an
// operation cannot appear in that operation's error text even when the
// message is composed by a vendor SDK. Only value redaction at the request
// scope can meet it; the regex net provably misses Value.
package sentinel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	githubconn "github.com/hollis-labs/cerberus/internal/connector/github"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	secret "github.com/hollis-labs/cerberus/pkg/secret"
)

// Value is the resolved credential: no provider prefix, no label, not
// Bearer-shaped. Rendered with no scope it survives redact.Text.
const Value = "q7Zr2mXv9pLwK4tN" //nolint:gosec // a test sentinel, not a credential

// Args is the operation the fixture fails: github status, a read that needs
// no acknowledgment.
func Args() map[string]any { return map[string]any{"owner": "hollis-labs", "repo": "cerberus"} }

// GitHubAPI is a GitHub API that rejects every request with a 401 whose
// message echoes the bearer token it was sent, unlabelled. go-github then
// composes that message into its error — vendor text around the credential.
func GitHubAPI(t testing.TB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials for " + token + " on this repo"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Registry registers the real GitHub connector the way app does — a factory
// that resolves github/token through provider on the operation's context —
// with its API backend pointed at apiURL.
func Registry(t testing.TB, provider secret.Provider, apiURL string) *connector.Registry {
	t.Helper()
	registry := connector.NewRegistry()
	registry.RegisterFactory(githubconn.Definition(), func(ctx context.Context) (contract.Connector, error) {
		token, err := secrets.WithContext(ctx, provider).Get(context.Background(), "github", "token")
		if err != nil {
			return nil, err
		}
		backend, err := githubconn.NewAPIBackendAt(token, apiURL+"/")
		if err != nil {
			return nil, err
		}
		return githubconn.NewWithBackend(backend), nil
	})
	return registry
}

// Provider holds Value as github/token behind secrets.Registering — the
// wrapper app.ConnectorSecrets uses — so resolving it registers it with the
// request's scope.
func Provider() secret.Provider {
	return secrets.Registering(staticProvider{"github/token": Value}, nil)
}

// AssertAbsent fails when rendered carries Value, and when it carries no
// redaction marker either — a surface that dropped the message entirely
// proves nothing.
func AssertAbsent(t testing.TB, surface, rendered string) {
	t.Helper()
	if strings.Contains(rendered, Value) {
		t.Errorf("%s: the resolved credential reached the output: %s", surface, rendered)
	}
	if !strings.Contains(rendered, redact.Marker) {
		t.Errorf("%s: no redaction marker, so nothing was proven: %s", surface, rendered)
	}
}

type staticProvider map[string]string

func (p staticProvider) Get(_ context.Context, service, key string) (string, error) {
	return p[service+"/"+key], nil
}
func (staticProvider) Set(context.Context, string, string, string) error { return nil }
func (staticProvider) Delete(context.Context, string, string) error      { return nil }

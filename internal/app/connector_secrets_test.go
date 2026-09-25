package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

const githubSentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential

// A credential the connector lane resolves is registered with the request's
// scope, through the same factory path Execute uses: Registry.Resolve builds
// the GitHub connector, which resolves its token.
func TestConnectorSecretsRegisterResolvedCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CERBERUS_GITHUB_TOKEN", githubSentinel)
	registry, _ := newConnectorRegistry(filepath.Join(t.TempDir(), "config.yaml"))
	ctx, scope := redact.EnsureScope(context.Background())
	if _, err := registry.Resolve(ctx, "github"); err != nil {
		t.Fatal(err)
	}
	if got := scope.Text("GET /user: 401 " + githubSentinel); got != "GET /user: 401 "+redact.Marker {
		t.Fatalf("the github token was not registered: %q", got)
	}
}

// Values that are not credentials are resolved without being registered,
// and which ones they are comes from the connector definitions (D1), not
// from this test.
func TestConnectorSecretsDoNotRegisterNonCredentials(t *testing.T) {
	if !builtInNonCredentials["ssh"]["key"] {
		t.Fatalf("the ssh definition's key secret is not declared as a path: %v", builtInNonCredentials)
	}
	for _, tc := range []struct{ service, key string }{{"ssh/box", "key"}, {"vercel", "scope"}} {
		if !notACredential(tc.service, tc.key) {
			t.Errorf("%s/%s would be registered", tc.service, tc.key)
		}
	}
	for _, tc := range []struct{ service, key string }{{"github", "token"}, {"ssh/box", "passphrase"}, {"vercel", "token"}, {"cloudflare", "api_token"}} {
		if notACredential(tc.service, tc.key) {
			t.Errorf("%s/%s is exempt, want it registered", tc.service, tc.key)
		}
	}

	const keyPath = "/Users/op/.ssh/id_ed25519_box"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CERBERUS_VERCEL_SCOPE", "acme-platform-team")
	t.Setenv("CERBERUS_SSH/BOX_KEY", keyPath)
	sec := ConnectorSecrets(filepath.Join(t.TempDir(), "config.yaml"))
	ctx, scope := redact.EnsureScope(context.Background())
	for _, tc := range []struct{ service, key, want string }{{"ssh/box", "key", keyPath}, {"vercel", "scope", "acme-platform-team"}} {
		if got, err := sec.Get(ctx, tc.service, tc.key); err != nil || got != tc.want {
			t.Fatalf("Get(%s/%s) = %q, %v", tc.service, tc.key, got, err)
		}
	}
	msg := "reading SSH key " + keyPath + ": no such file; deploying to acme-platform-team"
	if got := scope.Text(msg); got != msg {
		t.Fatalf("a non-credential was registered: %q", got)
	}
}

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/secret"
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

// Resolution cannot write (WP-S3). The chain every lane resolves through is a
// Reader and nothing more, so a read-only backend fits it and no resolving
// caller can reach Set or Delete; the console's form writes through
// SecretStore, the one writer.
func TestConnectorSecretsIsReadOnlyAndSecretStoreIsTheWriter(t *testing.T) {
	resolver := ConnectorSecrets(filepath.Join(t.TempDir(), "config.yaml"))
	if _, writable := resolver.(secret.ReadWriter); writable {
		t.Fatal("ConnectorSecrets exposes Set/Delete; resolution must be read-only")
	}
	if SecretStore() == nil {
		t.Fatal("SecretStore is nil; the console's provider form would silently drop credentials")
	}
}

// WP-S4's latent bug, end to end: a vault reference in the environment was
// handed to a connector as a literal token. Now it is a reference, and with no
// daemon to route it the lookup fails as credential_missing, naming the
// daemon; it never returns the reference as a value.
func TestAVaultReferenceIsNeverHandedOnAsAToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CERBERUS_GITHUB_TOKEN", "op://Deploy/GitHub/token")
	value, err := ConnectorSecrets(filepath.Join(t.TempDir(), "config.yaml")).Get(context.Background(), "github", "token")
	if err == nil || value != "" {
		t.Fatalf("Get = %q, %v; a vault reference must not resolve without a backend", value, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "credential_missing") || !strings.Contains(msg, "cerberus daemon start") {
		t.Fatalf("err = %v", err)
	}
	if got := redact.Text(msg); got != msg {
		t.Fatalf("the recovery does not survive redaction:\n  %s\n  %s", msg, got)
	}
}

// The core chain, which a secret backend's own credential comes from, has no
// vault in it.
func TestTheCoreChainRefusesVaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN", "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password")
	_, err := CoreConnectorSecrets(filepath.Join(t.TempDir(), "config.yaml")).Get(context.Background(), "onepassword", "service_account_token")
	if err == nil || !strings.Contains(err.Error(), "never from another vault") {
		t.Fatalf("err = %v", err)
	}
}

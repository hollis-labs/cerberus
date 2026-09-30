package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/secretref"
)

// Presence says where a secret stands without resolving it: a reference in
// the mapping or the keychain is reported as one, and the resolver, which
// would call a helper or a vault, is never run.
func TestPresenceNeverResolves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-secrets.yaml")
	if err := os.WriteFile(path, []byte("mapped:\n  token: helper://test-helper/vault/item/field\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := &referenceTestProvider{}
	provider := NewReferenceProvider(base, path)
	provider.resolver = secretref.NewResolver(base,
		secretref.WithHelperLookup(func(string) (string, error) { return "/helper", nil }),
		secretref.WithCommandRunner(func(context.Context, string, ...string) ([]byte, []byte, error) {
			t.Fatal("Presence resolved a reference")
			return nil, nil, nil
		}))
	ctx := context.Background()
	check := func(service string, want Presence) {
		t.Helper()
		got, err := Registering(provider, nil).(PresenceReader).Presence(ctx, service, "token")
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", service, got, err, want)
		}
	}
	check("mapped", PresenceReference)
	check("nothing", PresenceMissing)
	t.Setenv("CERBERUS_FROMENV_TOKEN", "a-value")
	check("fromenv", PresenceStored)
	base.value = "keyring-value"
	check("keychain", PresenceStored)
	base.value = "op://vault/item/field"
	check("keychain-ref", PresenceReference)
}

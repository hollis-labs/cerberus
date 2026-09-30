package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/secretref"
)

type labelledRouter struct{}

func (labelledRouter) Claims(string) bool { return false }
func (labelledRouter) ResolveSecret(_ context.Context, ref string) (string, error) {
	return "value-for-" + ref, nil
}
func (labelledRouter) Backend(scheme string) string { return "onepassword@0.1.0" }

// A reference's source names where it was found, its scheme and, for a
// vault, the plugin that resolved it; an unresolved one says so.
func TestSourcesNameTheVaultAndThePlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-secrets.yaml")
	if err := os.WriteFile(path, []byte("cloudflare:\n  api_token: op://Deploy/Cloudflare/token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var router BackendRouter
	router.Bind(labelledRouter{})
	p := NewReferenceProvider(nil, path, secretref.WithSchemeRouter(&router))
	ctx, sources := WithSources(context.Background())
	if _, err := p.Get(ctx, "cloudflare", "api_token"); err != nil {
		t.Fatal(err)
	}
	if got := sources.Snapshot()["cloudflare/api_token"]; got != "mapping:op via onepassword@0.1.0" {
		t.Fatalf("source = %q", got)
	}

	unbound := NewReferenceProvider(nil, path, secretref.WithSchemeRouter(&BackendRouter{}))
	if _, err := unbound.Get(ctx, "cloudflare", "api_token"); err == nil {
		t.Fatal("an unbound router resolved")
	}
	if got := sources.Snapshot()["cloudflare/api_token"]; got != "mapping:op, unresolved" {
		t.Fatalf("source = %q", got)
	}
}

// A per-access binding (I9) is its own source: the record names the binding,
// and an explicit null says so.
func TestSourcesNameTheBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-secrets.yaml")
	if err := os.WriteFile(path, []byte("cloudflare:\n  read: { api_token: op://Deploy/Cloudflare/ro }\n  write: { api_token: null }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var router BackendRouter
	router.Bind(labelledRouter{})
	p := NewReferenceProvider(nil, path, secretref.WithSchemeRouter(&router))

	ctx, sources := WithSources(context.Background())
	if _, err := p.Get(WithCredentialScope(ctx, CredentialScope{Access: AccessRead}), "cloudflare", "api_token"); err != nil {
		t.Fatal(err)
	}
	if got := sources.Snapshot()["cloudflare/api_token"]; got != "binding:read:op via onepassword@0.1.0" {
		t.Fatalf("read source = %q", got)
	}
	if _, err := p.Get(WithCredentialScope(ctx, CredentialScope{Access: AccessWrite}), "cloudflare", "api_token"); err == nil {
		t.Fatal("a null write binding resolved")
	}
	if got := sources.Snapshot()["cloudflare/api_token"]; got != "binding:write, none" {
		t.Fatalf("write source = %q", got)
	}
}

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/secretref"
	"github.com/chrispian/cerberus/pkg/secret"
)

type referenceTestProvider struct {
	secret.Provider
	value string
}

func (p *referenceTestProvider) Get(context.Context, string, string) (string, error) {
	return p.value, nil
}

func TestReferenceMappingsResolveFreshAndNeverAcceptLiterals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-secrets.yaml")
	base := &referenceTestProvider{value: "fallback"}
	provider := NewReferenceProvider(base, path)
	value, err := provider.Get(context.Background(), "test-connector", "token")
	if err != nil || value != "fallback" {
		t.Fatalf("fallback failed: %v", err)
	}
	resolved := "first-secret"
	provider.resolver = secretref.NewResolver(base, secretref.WithHelperLookup(func(string) (string, error) { return "/helper", nil }), secretref.WithCommandRunner(func(ctx context.Context, _ string, args ...string) ([]byte, []byte, error) {
		if args[0] != "resolve" || args[1] != "keychain://vault/item/field" {
			t.Fatalf("bad helper reference: %v", args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("reference lookup unbounded")
		}
		return []byte(resolved), nil, ctx.Err()
	}))
	if err = os.WriteFile(path, []byte("test-connector:\n  token: helper://test-helper/vault/item/field\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err = provider.Get(context.Background(), "test-connector", "token")
	if err != nil || value != resolved {
		t.Fatalf("reference not resolved: %v", err)
	}
	resolved = "rotated-secret"
	value, err = provider.Get(context.Background(), "test-connector", "token")
	if err != nil || value != resolved {
		t.Fatalf("cached old credential: %v", err)
	}
	if err = os.WriteFile(path, []byte("test-connector:\n  token: plaintext-must-not-leak\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Get(context.Background(), "test-connector", "token")
	if err == nil || strings.Contains(err.Error(), "plaintext-must-not-leak") {
		t.Fatalf("literal accepted or leaked: %v", err)
	}
}

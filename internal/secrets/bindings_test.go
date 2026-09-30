package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type mapReader map[string]string

func (m mapReader) Get(_ context.Context, service, key string) (string, error) {
	return m[service+"/"+key], nil
}

const bindingsYAML = `
cloudflare:
  api_token: keychain://cloudflare/legacy
  read:  { api_token: keychain://cloudflare/ro }
  write: { api_token: keychain://cloudflare/rw }
  targets:
    - match: { env: prod }
      read:  { api_token: keychain://cloudflare/prod-ro }
      write: { api_token: null }
github:
  token: keychain://github/token
`

func provider(t *testing.T, data string) *ReferenceProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connector-secrets.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	base := mapReader{"cloudflare/legacy": "LEGACY", "cloudflare/ro": "RO", "cloudflare/rw": "RW", "cloudflare/prod-ro": "PROD-RO", "github/token": "GH"}
	return NewReferenceProvider(base, path)
}

func scoped(access Access, env target.Env) context.Context {
	return WithCredentialScope(context.Background(), CredentialScope{Access: access, Target: target.Target{Kind: "cloudflare.zone", ID: "zone-1", Labels: target.Labels{Env: env}}})
}

// Each access gets its own binding; a target's binding wins at its level
// for both accesses; null refuses without falling back; an unscoped call
// and an unsplit connector keep the legacy chain.
func TestBindingResolution(t *testing.T) {
	p := provider(t, bindingsYAML)
	for _, c := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"read on dev", scoped(AccessRead, target.EnvDev), "RO"},
		{"write on dev", scoped(AccessWrite, target.EnvDev), "RW"},
		{"read on prod", scoped(AccessRead, target.EnvProd), "PROD-RO"},
		{"no scope", context.Background(), "LEGACY"},
	} {
		got, err := p.Get(c.ctx, "cloudflare", "api_token")
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v", c.name, got, err)
		}
	}
	_, err := p.Get(scoped(AccessWrite, target.EnvProd), "cloudflare", "api_token")
	var none *NoCredentialError
	if !errors.As(err, &none) || !errors.Is(err, ErrNoCredential) || none.Label != "targets[0].write" ||
		!strings.Contains(err.Error(), "the read credential is never used instead") {
		t.Fatalf("a prod write: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
	if got, err := p.Get(scoped(AccessWrite, target.EnvProd), "github", "token"); err != nil || got != "GH" {
		t.Fatalf("an unsplit connector: %q %v", got, err)
	}
	// An environment value cannot stand in under a per-access binding.
	t.Setenv("CERBERUS_CLOUDFLARE_API_TOKEN", "FROM-ENV")
	if got, _ := p.Get(scoped(AccessRead, target.EnvDev), "cloudflare", "api_token"); got != "RO" {
		t.Fatalf("env overrode a read binding: %q", got)
	}
	if got, _ := p.Get(context.Background(), "cloudflare", "api_token"); got != "FROM-ENV" {
		t.Fatalf("env no longer leads the legacy chain: %q", got)
	}
}

// Declaring either half needs the other (or null); targets need a match;
// values are references only; an old flat file still parses.
func TestBindingValidation(t *testing.T) {
	for want, data := range map[string]string{ //nolint:gosec // reference fixtures, not credentials
		"binds api_token for read and not for write":  "cf:\n  read: { api_token: keychain://cf/ro }\n",
		"binds api_token for write and not for read":  "cf:\n  targets:\n    - match: { env: prod }\n      write: { api_token: keychain://cf/rw }\n",
		"an empty match would cover every target":     "cf:\n  targets:\n    - match: {}\n      read: { k: null }\n      write: { k: null }\n",
		"literal credentials are not allowed":         "cf:\n  read: { k: plaintext }\n  write: { k: null }\n",
		"read, write and targets are the only nested": "cf:\n  other: { k: keychain://a/b }\n",
	} {
		if _, err := ParseBindings([]byte(data), "f", secretref.IsRef); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q: %v", want, err)
		}
	}
	file, err := ParseBindings([]byte("namecheap:\n  api_key: keychain://namecheap/api_key\n"), "f", secretref.IsRef)
	if err != nil || file["namecheap"].Split() || file["namecheap"].Flat["api_key"] != "keychain://namecheap/api_key" {
		t.Fatalf("a flat file: %+v %v", file, err)
	}
}

func TestAccessFor(t *testing.T) {
	for _, c := range []struct {
		effect  contract.Effect
		preview bool
		want    Access
	}{
		{contract.EffectRead, false, AccessRead},
		{contract.EffectReadSensitive, false, AccessRead},
		{contract.EffectWrite, false, AccessWrite},
		{contract.EffectDestructive, true, AccessRead},
		{"", false, AccessWrite},
	} {
		if got := AccessFor(c.effect, c.preview); got != c.want {
			t.Errorf("%s preview=%v: %s", c.effect, c.preview, got)
		}
	}
}

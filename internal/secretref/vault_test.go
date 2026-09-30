package secretref

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// The latent bug WP-S4 closes: an op:// value was not a reference, so it was
// handed on as a literal credential, sent to a provider as a token. Now it is
// a reference whether or not a backend is installed, and without one it
// fails; it is never returned as a value.
func TestAVaultReferenceIsNeverALiteral(t *testing.T) {
	for _, ref := range []string{"op://Deploy/Database/password", "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"} {
		if !IsRef(ref) {
			t.Fatalf("%s is not a reference", ref)
		}
		got, err := NewResolver(&stubProvider{}).Resolve(context.Background(), ref)
		if err == nil || got != "" {
			t.Fatalf("%s resolved to %q with no backend", ref, got)
		}
		if !errors.Is(err, ErrNoSecretBackend) {
			t.Fatalf("%s: %v, want ErrNoSecretBackend", ref, err)
		}
		if strings.Contains(err.Error(), "Deploy/Database") || strings.Contains(err.Error(), "AbCdEfGh") {
			t.Fatalf("the error carries the reference path: %v", err)
		}
		env, err := NewResolver(&stubProvider{}).ResolveEnv(context.Background(), map[string]string{"TOKEN": ref})
		if err == nil || env != nil {
			t.Fatalf("ResolveEnv passed %s through: %v %v", ref, env, err)
		}
	}
}

// keyring:// is keychain:// under its platform-neutral name.
func TestKeyringIsKeychain(t *testing.T) {
	p := &stubProvider{values: map[string]string{"openai/work": "sk-from-the-store"}}
	for _, ref := range []string{"keyring://openai/work", "keychain://openai/work"} {
		got, err := NewResolver(p).Resolve(context.Background(), ref)
		if err != nil || got != "sk-from-the-store" {
			t.Fatalf("%s = %q, %v", ref, got, err)
		}
	}
}

type fakeRouter struct {
	claims map[string]bool
	values map[string]string
	err    error
	asked  []string
}

func (f *fakeRouter) Claims(scheme string) bool { return f.claims[scheme] }
func (f *fakeRouter) ResolveSecret(_ context.Context, ref string) (string, error) {
	f.asked = append(f.asked, ref)
	if f.err != nil {
		return "", f.err
	}
	return f.values[ref], nil
}

func TestVaultReferencesGoToTheRouter(t *testing.T) {
	router := &fakeRouter{values: map[string]string{
		"op://Deploy/Database/password":  "from-1password",
		"keeper://AbCdEf/field/password": "from-keeper",
	}}
	r := NewResolver(&stubProvider{}, WithSchemeRouter(router))
	for ref, want := range router.values {
		if !r.IsRef(ref) {
			t.Fatalf("%s is not a reference to the resolver", ref)
		}
		got, err := r.Resolve(context.Background(), ref)
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v", ref, got, err)
		}
	}
	// No fallback: a failed backend is the answer.
	router.err = errors.New("credential_missing: vault unreachable")
	if got, err := r.Resolve(context.Background(), "op://Deploy/Database/password"); err == nil || got != "" {
		t.Fatalf("a failed backend resolved: %q", got)
	}
	// A backend that answers with another reference is refused.
	router.err = nil
	router.values["op://Deploy/Database/password"] = "keychain://other/secret"
	if _, err := r.Resolve(context.Background(), "op://Deploy/Database/password"); !errors.Is(err, ErrResolvedToRef) {
		t.Fatalf("err = %v", err)
	}
}

// A router that claims a scheme whose values carry credentials does not get
// them: postgres://user:password@host stays a literal, never a reference
// sent to a plugin (M5).
func TestAClaimedURLSchemeIsNotAReference(t *testing.T) {
	router := &fakeRouter{claims: map[string]bool{"postgres": true, "s3": true, "ssh": true}}
	r := NewResolver(&stubProvider{}, WithSchemeRouter(router))
	env := map[string]string{ //nolint:gosec // credential-shaped URLs are the point of the test
		"DATABASE_URL": "postgres://app:hunter2hunter2@db:5432/app",
		"BUCKET":       "s3://key:secret@bucket",
		"GIT":          "ssh://git@host/repo",
	}
	for _, v := range env {
		if r.IsRef(v) {
			t.Fatalf("%s is a reference", v)
		}
	}
	out, err := r.ResolveEnv(context.Background(), env)
	if err != nil || out["DATABASE_URL"] != env["DATABASE_URL"] {
		t.Fatalf("ResolveEnv = %v, %v", out, err)
	}
	if len(router.asked) != 0 {
		t.Fatalf("the router was sent %v", router.asked)
	}
}

// The core chain names why a vault is out of reach.
func TestWithoutSchemeRouterExplains(t *testing.T) {
	r := NewResolver(&stubProvider{}, WithoutSchemeRouter("backends take their own credential from the OS store"))
	_, err := r.Resolve(context.Background(), "op://Deploy/Database/password")
	if err == nil || !strings.Contains(err.Error(), "backends take their own credential from the OS store") {
		t.Fatalf("err = %v", err)
	}
}

// redact keeps its own copy of the schemes (it cannot import this package).
// A reference is a name, not a credential, so every scheme must be in it.
func TestRedactKnowsEveryReferenceScheme(t *testing.T) {
	for _, scheme := range Schemes() {
		if !redact.IsReference(scheme + "://x/y") {
			t.Errorf("redact.IsReference does not know %s://", scheme)
		}
	}
}

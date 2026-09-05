package secretref

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubProvider struct {
	values map[string]string
	err    error
	seen   []string
}

func (s *stubProvider) Get(_ context.Context, service, key string) (string, error) {
	s.seen = append(s.seen, service+"/"+key)
	if s.err != nil {
		return "", s.err
	}
	return s.values[service+"/"+key], nil
}

func TestIsRef(t *testing.T) {
	refs := []string{"keychain://openai/work", "helper://mux-apikey-helper/openai/work"}
	for _, v := range refs {
		if !IsRef(v) {
			t.Errorf("IsRef(%q) = false, want true", v)
		}
	}
	literals := []string{"", "sk-literal-value", "https://example.com/x", "op://vault/item/field"}
	for _, v := range literals {
		if IsRef(v) {
			t.Errorf("IsRef(%q) = true, want false", v)
		}
	}
}

func TestParse(t *testing.T) {
	t.Run("keychain ref", func(t *testing.T) {
		ref, err := Parse("keychain://anthropic/claude-code-oauth")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ref.Scheme != "keychain" || ref.Service != "anthropic" || ref.Key != "claude-code-oauth" {
			t.Errorf("Parse = %+v", ref)
		}
	})

	t.Run("helper ref keeps the multi-segment key", func(t *testing.T) {
		ref, err := Parse("helper://mux-apikey-helper/openai/work")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ref.Service != "mux-apikey-helper" || ref.Key != "openai/work" {
			t.Errorf("Parse = %+v", ref)
		}
	})

	t.Run("rejects malformed refs", func(t *testing.T) {
		bad := []string{
			"keychain://openai",       // no key
			"keychain:///work",        // no service
			"keychain://openai//work", // empty segment
			"helper://../openai/work", // path traversal in the helper name
		}
		for _, v := range bad {
			if _, err := Parse(v); err == nil {
				t.Errorf("Parse(%q) succeeded, want error", v)
			}
		}
	})

	t.Run("literal is not a reference", func(t *testing.T) {
		_, err := Parse("sk-literal")
		if !errors.Is(err, ErrNotAReference) {
			t.Errorf("err = %v, want ErrNotAReference", err)
		}
	})
}

func TestResolverKeychain(t *testing.T) {
	t.Run("resolves through the provider", func(t *testing.T) {
		p := &stubProvider{values: map[string]string{"openai/work": "resolved-key"}}
		got, err := NewResolver(p).Resolve(context.Background(), "keychain://openai/work")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "resolved-key" {
			t.Errorf("Resolve = %q", got)
		}
	})

	t.Run("missing entry is an error, not an empty credential", func(t *testing.T) {
		p := &stubProvider{values: map[string]string{}}
		_, err := NewResolver(p).Resolve(context.Background(), "keychain://openai/work")
		if !errors.Is(err, ErrEmptySecret) {
			t.Fatalf("err = %v, want ErrEmptySecret", err)
		}
		if !strings.Contains(err.Error(), "keychain://openai/work") {
			t.Errorf("error does not name the reference: %v", err)
		}
	})

	t.Run("provider failure does not leak the secret", func(t *testing.T) {
		p := &stubProvider{values: map[string]string{"openai/work": "sk-super-secret"}, err: errors.New("keychain locked")}
		_, err := NewResolver(p).Resolve(context.Background(), "keychain://openai/work")
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), "sk-super-secret") {
			t.Errorf("error leaked secret material: %v", err)
		}
	})
}

func TestResolverHelper(t *testing.T) {
	t.Run("delegates with the keychain scheme", func(t *testing.T) {
		var gotArgs []string
		r := NewResolver(nil,
			WithHelperLookup(func(name string) (string, error) { return "/usr/local/bin/" + name, nil }),
			WithCommandRunner(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
				gotArgs = append([]string{name}, args...)
				return []byte("helper-resolved\n"), nil, nil
			}),
		)
		got, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "helper-resolved" {
			t.Errorf("Resolve = %q", got)
		}
		want := []string{"/usr/local/bin/mux-apikey-helper", "resolve", "keychain://openai/work"}
		if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
			t.Errorf("helper invoked as %v, want %v", gotArgs, want)
		}
	})

	t.Run("missing helper is reported", func(t *testing.T) {
		r := NewResolver(nil, WithHelperLookup(func(string) (string, error) { return "", errors.New("not found") }))
		_, err := r.Resolve(context.Background(), "helper://absent-helper/openai/work")
		if !errors.Is(err, ErrHelperNotFound) {
			t.Errorf("err = %v, want ErrHelperNotFound", err)
		}
	})
}

func TestResolveEnv(t *testing.T) {
	p := &stubProvider{values: map[string]string{"openai/work": "resolved-key"}}
	r := NewResolver(p)

	env := map[string]string{
		"OPENAI_API_KEY": "keychain://openai/work",
		"PATH":           "/usr/bin",
		"PLAIN":          "literal-value",
	}
	got, err := r.ResolveEnv(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["OPENAI_API_KEY"] != "resolved-key" {
		t.Errorf("OPENAI_API_KEY = %q", got["OPENAI_API_KEY"])
	}
	if got["PATH"] != "/usr/bin" || got["PLAIN"] != "literal-value" {
		t.Errorf("literals were altered: %+v", got)
	}
	if env["OPENAI_API_KEY"] != "keychain://openai/work" {
		t.Error("ResolveEnv mutated its input")
	}
	if len(p.seen) != 1 {
		t.Errorf("provider consulted %d times, want 1 (literals must not be resolved)", len(p.seen))
	}
}

func TestEnvHasRefs(t *testing.T) {
	if EnvHasRefs(map[string]string{"A": "literal", "B": "/usr/bin"}) {
		t.Error("EnvHasRefs = true for a literal-only environment")
	}
	if !EnvHasRefs(map[string]string{"A": "literal", "B": "keychain://openai/work"}) {
		t.Error("EnvHasRefs = false when a reference is present")
	}
}

// TestLookHelperPathFindsHelperOutsideMinimalPATH covers the launchd case: a
// managed service inherits PATH=/usr/bin:/bin:/usr/sbin:/sbin, so a helper
// installed under ~/go/bin must still be found.
func TestLookHelperPathFindsHelperOutsideMinimalPATH(t *testing.T) {
	home := t.TempDir()
	gobin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(gobin, 0o750); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(gobin, "test-apikey-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	t.Setenv("HOME", home)
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "")

	got, err := lookHelperPath("test-apikey-helper")
	if err != nil {
		t.Fatalf("lookHelperPath: %v", err)
	}
	if got != helper {
		t.Errorf("lookHelperPath = %q, want %q", got, helper)
	}
}

func TestLookHelperPathRejectsNonExecutable(t *testing.T) {
	home := t.TempDir()
	gobin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(gobin, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gobin, "not-exec"), []byte("data"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", home)
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "")

	if _, err := lookHelperPath("not-exec"); !errors.Is(err, ErrHelperNotFound) {
		t.Errorf("err = %v, want ErrHelperNotFound", err)
	}
}

// TestSanitizedEnvironDropsReferenceValues guards the echo-back hazard: a
// helper that prefers a conventional environment variable over the keychain
// must not be handed the very reference it is being asked to resolve.
func TestSanitizedEnvironDropsReferenceValues(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "keychain://openai/work")
	t.Setenv("ANTHROPIC_API_KEY", "helper://mux-apikey-helper/anthropic/default")
	t.Setenv("SECRETREF_TEST_PLAIN", "a-literal-value")

	got := sanitizedEnviron()
	for _, entry := range got {
		name, value, _ := strings.Cut(entry, "=")
		if IsRef(value) {
			t.Errorf("sanitizedEnviron kept reference-valued %s", name)
		}
	}

	var sawPlain bool
	for _, entry := range got {
		if entry == "SECRETREF_TEST_PLAIN=a-literal-value" {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Error("sanitizedEnviron dropped a literal variable; only references should be removed")
	}
}

// TestResolveDecodesGoKeyringEncodedSecret covers the interop hazard from the
// helper:// path: an older mux-apikey-helper reads via the `security` CLI and
// returns go-keyring's storage marker verbatim.
func TestResolveDecodesGoKeyringEncodedSecret(t *testing.T) {
	const secret = "resolved-value-for-test"
	encoded := keyringBase64Prefix + base64.StdEncoding.EncodeToString([]byte(secret))

	r := NewResolver(nil,
		WithHelperLookup(func(name string) (string, error) { return "/usr/local/bin/" + name, nil }),
		WithCommandRunner(func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(encoded + "\n"), nil, nil
		}),
	)
	got, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != secret {
		t.Errorf("Resolve = %q, want the decoded secret", got)
	}
	if strings.Contains(got, keyringBase64Prefix) {
		t.Error("the go-keyring marker survived into the resolved value")
	}
}

func TestResolveRejectsCorruptEncodedSecret(t *testing.T) {
	r := NewResolver(nil,
		WithHelperLookup(func(name string) (string, error) { return "/usr/local/bin/" + name, nil }),
		WithCommandRunner(func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(keyringBase64Prefix + "!!!not-base64!!!"), nil, nil
		}),
	)
	if _, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work"); !errors.Is(err, ErrEncodedSecret) {
		t.Errorf("err = %v, want ErrEncodedSecret", err)
	}
}

// TestResolveRejectsEchoedReference is the belt-and-braces guard for the
// helper env echo-back: a helper that answers from a conventional environment
// variable can hand back the reference it was asked to resolve. Accepting that
// starts a service with a valid-looking, entirely wrong credential.
func TestResolveRejectsEchoedReference(t *testing.T) {
	for _, echoed := range []string{
		"keychain://openai/work",
		"helper://mux-apikey-helper/openai/work",
	} {
		r := NewResolver(nil,
			WithHelperLookup(func(name string) (string, error) { return "/usr/local/bin/" + name, nil }),
			WithCommandRunner(func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
				return []byte(echoed), nil, nil
			}),
		)
		_, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work")
		if !errors.Is(err, ErrResolvedToRef) {
			t.Errorf("echoed %q: err = %v, want ErrResolvedToRef", echoed, err)
		}
	}
}

// A keychain:// resolution that somehow yields a reference is caught too.
func TestResolveRejectsEchoedReferenceFromProvider(t *testing.T) {
	p := &stubProvider{values: map[string]string{"openai/work": "keychain://openai/work"}}
	if _, err := NewResolver(p).Resolve(context.Background(), "keychain://openai/work"); !errors.Is(err, ErrResolvedToRef) {
		t.Errorf("err = %v, want ErrResolvedToRef", err)
	}
}

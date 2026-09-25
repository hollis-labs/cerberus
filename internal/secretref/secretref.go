// Package secretref resolves secret references that appear in resource
// configuration.
//
// Cerberus writes a managed service's environment into a launchd plist, so any
// literal credential in ~/.cerberus/projects/*.cerberus.yaml is copied verbatim
// into ~/Library/LaunchAgents/*.plist — two plaintext copies of the same secret
// in files that operators and agents routinely read. A reference keeps both
// files free of credential material: the plist carries the reference, and the
// `cerberus run-secrets` shim resolves it in the moment the service execs.
//
// Two schemes are supported:
//
//	keychain://<service>/<key>              → the Cerberus login-keychain entry
//	helper://<helper>/<authority>/<path>    → `<helper> resolve keychain://<authority>/<path>`
//
// The helper scheme exists so a secret shared with another Hollis Labs app does
// not need a second copy in a second keychain namespace. Tether's OpenAI key,
// for instance, is reachable as helper://mux-apikey-helper/openai/work, which
// resolves the same single keychain entry Tether's own resolver reads.
package secretref

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Errors returned by Parse and Resolver.
var (
	ErrNotAReference     = errors.New("not a secret reference")
	ErrUnsupportedScheme = errors.New("unsupported secret reference scheme")
	ErrEmptySecret       = errors.New("secret resolved to an empty value")
	ErrHelperNotFound    = errors.New("secret helper not found")
	ErrResolvedToRef     = errors.New("secret resolved to another secret reference")
	ErrEncodedSecret     = errors.New("secret is go-keyring-encoded and could not be decoded")
)

// keyringBase64Prefix is the marker zalando/go-keyring writes ahead of a
// base64-encoded secret on macOS. go-keyring's own reader strips it; a reader
// going through the `security` CLI does not. See decodeKeyringValue.
const keyringBase64Prefix = "go-keyring-base64:"

// Scheme prefixes recognized by IsRef and Parse.
const (
	keychainScheme = "keychain://"
	helperScheme   = "helper://"
)

// Ref is a parsed secret reference.
type Ref struct {
	Raw    string
	Scheme string
	// Service is the keychain service for keychain:// refs, and the helper
	// executable name for helper:// refs.
	Service string
	// Key is the remainder of the path.
	Key string
}

// IsRef reports whether value carries a scheme Parse understands. Values that
// are not references are literals and are passed through untouched, so an
// existing configuration of plain values keeps working.
func IsRef(value string) bool {
	return strings.HasPrefix(value, keychainScheme) || strings.HasPrefix(value, helperScheme)
}

// Parse validates a secret reference.
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if !IsRef(raw) {
		return Ref{}, fmt.Errorf("%w: %q", ErrNotAReference, raw)
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	service, key, ok := strings.Cut(rest, "/")
	if !ok || service == "" || key == "" {
		return Ref{}, fmt.Errorf("parse %q: expected %s://<service>/<key>", raw, scheme)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" {
			return Ref{}, fmt.Errorf("parse %q: empty path segment", raw)
		}
	}
	if scheme == "helper" && (strings.ContainsAny(service, `/\`) || service == "." || service == "..") {
		return Ref{}, fmt.Errorf("parse %q: helper name must not contain a path", raw)
	}
	return Ref{Raw: raw, Scheme: scheme, Service: service, Key: key}, nil
}

// Provider reads a secret from the OS keychain. It matches the Get half of
// internal/secrets.KeychainProvider so the existing provider satisfies it.
type Provider interface {
	Get(ctx context.Context, service, key string) (string, error)
}

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)

// Resolver resolves secret references.
type Resolver struct {
	provider   Provider
	lookHelper func(string) (string, error)
	run        commandRunner
}

// Option customizes a Resolver.
type Option func(*Resolver)

// WithHelperLookup overrides how helper names map to executable paths.
func WithHelperLookup(fn func(string) (string, error)) Option {
	return func(r *Resolver) { r.lookHelper = fn }
}

// WithCommandRunner overrides helper execution.
func WithCommandRunner(fn func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)) Option {
	return func(r *Resolver) { r.run = fn }
}

// NewResolver returns a Resolver backed by provider for keychain:// refs and by
// local helper execution for helper:// refs.
func NewResolver(provider Provider, opts ...Option) *Resolver {
	r := &Resolver{provider: provider, lookHelper: lookHelperPath, run: runCommand}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns the secret behind ref.
//
// The resolved value is never placed in the returned error — only the
// reference itself, which is non-sensitive by construction.
func (r *Resolver) Resolve(ctx context.Context, raw string) (string, error) {
	ref, err := Parse(raw)
	if err != nil {
		return "", err
	}
	var secret string
	switch ref.Scheme {
	case "keychain":
		if r.provider == nil {
			return "", fmt.Errorf("resolve %s: no keychain provider configured", ref.Raw)
		}
		secret, err = r.provider.Get(ctx, ref.Service, ref.Key)
	case "helper":
		secret, err = r.resolveHelper(ctx, ref)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedScheme, ref.Scheme)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, err)
	}
	// KeychainProvider.Get reports a missing entry as ("", nil). For a
	// reference that is explicitly asking for a secret, absence is an error:
	// handing an empty credential to a service produces failures far from
	// their cause.
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, ErrEmptySecret)
	}
	if secret, err = decodeKeyringValue(secret); err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, err)
	}
	// Guard against a resolver handing back a reference instead of a secret.
	// A helper that consults a conventional environment variable before the
	// keychain will echo the reference straight back when that variable holds
	// it — see sanitizedEnviron, which prevents the common case. This catches
	// any other path to the same outcome, which is otherwise indistinguishable
	// from success until the credential is used.
	if IsRef(secret) {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, ErrResolvedToRef)
	}
	return secret, nil
}

// decodeKeyringValue reverses zalando/go-keyring's macOS storage encoding.
//
// Cerberus resolves keychain:// through go-keyring, which decodes this marker
// itself, so this is not needed on that path. It matters for helper://, where
// an older helper binary reading via the `security` CLI returns the marker
// verbatim. Undecoded, that string has the length and shape of a credential
// and fails only later, at the API call.
func decodeKeyringValue(value string) (string, error) {
	if !strings.HasPrefix(value, keyringBase64Prefix) {
		return value, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, keyringBase64Prefix))
	if err != nil {
		return "", ErrEncodedSecret
	}
	return strings.TrimSpace(string(decoded)), nil
}

func (r *Resolver) resolveHelper(ctx context.Context, ref Ref) (string, error) {
	path, err := r.lookHelper(ref.Service)
	if err != nil || path == "" {
		return "", fmt.Errorf("%w: %s", ErrHelperNotFound, ref.Service)
	}
	// Helpers speak the keychain:// scheme; the helper:// authority names the
	// helper, not the secret, so it is dropped from the delegated reference.
	delegated := keychainScheme + ref.Key
	stdout, _, err := r.run(ctx, path, "resolve", delegated)
	if err != nil {
		// The helper's stderr is not copied into the error. It is text
		// Cerberus did not compose from a program that holds the secret,
		// and the error travels to launchd's stderr.log and to every
		// surface. The command that shows it is named instead.
		return "", fmt.Errorf("helper %s failed (%w); run `%s resolve %s` to see its output", ref.Service, err, path, delegated)
	}
	return string(stdout), nil
}

// ResolveEnv returns a copy of env with every reference value resolved.
// Literal values are copied through unchanged.
func (r *Resolver) ResolveEnv(ctx context.Context, env map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for key, value := range env {
		if !IsRef(value) {
			out[key] = value
			continue
		}
		resolved, err := r.Resolve(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = resolved
	}
	return out, nil
}

// EnvHasRefs reports whether any value in env is a secret reference.
func EnvHasRefs(env map[string]string) bool {
	for _, value := range env {
		if IsRef(value) {
			return true
		}
	}
	return false
}

// helperSearchDirs are the locations checked after $PATH when locating a
// helper binary.
//
// This matters specifically because of launchd: a managed service inherits a
// minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin), so a helper installed in the
// usual Go or Homebrew bin directory is invisible to a plain LookPath. Without
// these fallbacks the helper:// scheme would work from a shell and fail in the
// exact context it exists to serve.
func helperSearchDirs() []string {
	var dirs []string
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		dirs = append(dirs, filepath.Join(gopath, "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"))
	}
	// Sibling of the running executable — a Cerberus installed alongside its
	// helpers finds them without any environment at all.
	if exe, err := os.Executable(); err == nil {
		if eval, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
			exe = eval
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

// lookHelperPath resolves a bare helper name to an executable path, falling
// back past $PATH to the well-known install locations.
func lookHelperPath(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil && isExecutableFile(path) {
		return path, nil
	}
	for _, dir := range helperSearchDirs() {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrHelperNotFound, name)
}

// isExecutableFile reports whether path is a regular file with an execute bit.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path) //nolint:gosec // operator-configured helper path is the intended trust boundary
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// sanitizedEnviron returns the current environment with every reference-valued
// variable removed.
//
// This is load-bearing, not hygiene. Helpers commonly consult a conventional
// environment variable before the keychain — mux-apikey-helper answers a
// keychain://openai/... reference from $OPENAI_API_KEY when that is set. The
// shim's own environment is the service environment, where OPENAI_API_KEY holds
// the very reference being resolved, so an unfiltered hand-off makes the helper
// echo the reference string straight back and the service starts with its own
// reference as an API key: a silent, valid-looking, entirely wrong credential.
//
// Dropping reference-valued variables leaves genuine env overrides intact while
// closing the echo.
func sanitizedEnviron() []string {
	entries := os.Environ()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, value, ok := strings.Cut(entry, "="); ok && IsRef(value) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // helper path comes from LookPath on an operator-configured name
	cmd.Env = sanitizedEnviron()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

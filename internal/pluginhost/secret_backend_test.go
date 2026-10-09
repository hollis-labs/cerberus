package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

const backendValue = "vR8kQ3mZ7wLp2xTn" //nolint:gosec // a test sentinel, not a credential

// backendProcess is a loaded secret backend: it answers command/execute.
type backendProcess struct {
	fakeProcess
	mu       sync.Mutex
	requests []SDKCommandRequest
	result   SDKCommandResult
	err      error
}

func (p *backendProcess) Command(_ context.Context, req SDKCommandRequest) (SDKCommandResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return p.result, p.err
}

func backendPlugin(id, scheme string) InstalledPlugin {
	p := validInstalledPlugin()
	p.ID = id
	p.Manifest.ID = id
	p.Manifest.Config.Secrets = []contract.SecretRequirement{{Name: "token", Required: true}}
	p.Spec.ID = id
	p.Spec.Cerberus.Connector.ID = id
	p.Spec.Cerberus.Connector.Config.Secrets = p.Manifest.Config.Secrets
	p.Spec.Cerberus.SecretBackend = &plugin.SecretBackend{Scheme: scheme, Reference: scheme + "://<vault>/<item>/<field>"}
	return p
}

func loadedBackend(t *testing.T, process Process, opts ...ManagerOption) *Manager {
	t.Helper()
	p := backendPlugin("onepassword", "op")
	m := newTestManager(t, nil, fakeLauncher{process: process}, "test", opts...)
	m.RegisterInstalled(p)
	if err := m.Load(context.Background(), p.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

func newBackendProcess(result SDKCommandResult) *backendProcess {
	return &backendProcess{
		fakeProcess: fakeProcess{initResult: SDKInitResult{CapabilityContract: 1, ID: "onepassword", Version: "dev", Protocol: SDKProtocolVersion}},
		result:      result,
	}
}

func TestResolveSecretRoutesToTheClaimingBackend(t *testing.T) {
	process := newBackendProcess(SDKCommandResult{Action: plugin.ResolveActionValue, Content: backendValue})
	m := loadedBackend(t, process)

	if got := m.SchemeClaimant("op"); got != "onepassword" {
		t.Fatalf("claimant = %q", got)
	}
	value, err := m.ResolveSecret(context.Background(), "op://Deploy/Database/password")
	if err != nil || value != backendValue {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
	if len(process.requests) != 1 || process.requests[0].Name != plugin.ResolveCommand {
		t.Fatalf("requests = %+v", process.requests)
	}
	var args plugin.ResolveArgs
	if err := json.Unmarshal([]byte(process.requests[0].Args), &args); err != nil || args.Ref != "op://Deploy/Database/password" {
		t.Fatalf("args = %q", process.requests[0].Args)
	}

	// The value joins the backend's own redactor: its later text loses it.
	m.mu.RLock()
	lp := m.running["onepassword"]
	m.mu.RUnlock()
	if got := lp.currentRedactor().Text("status: last value " + backendValue); strings.Contains(got, backendValue) {
		t.Fatalf("the backend's redactor does not hold the value it resolved: %q", got)
	}
}

// A backend's own failure is credential_missing with its explanation, which
// passes through the backend's redactor.
func TestResolveSecretReportsTheBackendsFailureRedacted(t *testing.T) {
	process := newBackendProcess(SDKCommandResult{Action: plugin.ResolveActionValue, Content: backendValue})
	m := loadedBackend(t, process)
	if _, err := m.ResolveSecret(context.Background(), "op://a/b/c"); err != nil {
		t.Fatal(err)
	}
	process.result = SDKCommandResult{Action: plugin.ResolveActionError,
		Content: string(plugin.ErrorResult(plugin.ErrorCredentialMissing, "1Password sign-in failed after "+backendValue).Content)}
	_, err := m.ResolveSecret(context.Background(), "op://a/b/c")
	var backendErr *SecretBackendError
	if !errors.As(err, &backendErr) || backendErr.ErrorCode() != "credential_missing" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), backendValue) || !strings.Contains(err.Error(), "sign-in failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveSecretFailsClosedWithTheRecoveryNamed(t *testing.T) {
	unloaded := newTestManager(t, nil, fakeLauncher{process: newBackendProcess(SDKCommandResult{})}, "test")
	unloaded.RegisterInstalled(backendPlugin("onepassword", "op"))
	plainLoaded := newTestManager(t, nil, fakeLauncher{process: &fakeProcess{initResult: SDKInitResult{CapabilityContract: 1, ID: "onepassword", Version: "dev", Protocol: SDKProtocolVersion}}}, "test")
	plainLoaded.RegisterInstalled(backendPlugin("onepassword", "op"))
	if err := plainLoaded.Load(context.Background(), "onepassword"); err != nil {
		t.Fatal(err)
	}
	empty := loadedBackend(t, newBackendProcess(SDKCommandResult{Action: plugin.ResolveActionValue}))
	odd := loadedBackend(t, newBackendProcess(SDKCommandResult{Action: "noop"}))
	broken := loadedBackend(t, &backendProcess{
		fakeProcess: fakeProcess{initResult: SDKInitResult{CapabilityContract: 1, ID: "onepassword", Version: "dev", Protocol: SDKProtocolVersion}},
		err:         errors.New("pipe closed"),
	})

	cases := []struct {
		name string
		m    *Manager
		ref  string
		want string
	}{
		{"no claimant", newTestManager(t, nil, fakeLauncher{}, "test"), "op://a/b/c", "cerberus connectors plugin managed install"},
		{"not a reference", unloaded, "not-a-ref", "not a <scheme>:// reference"},
		{"installed, not loaded", unloaded, "op://a/b/c", "cerberus connectors plugin managed load onepassword"},
		{"no command transport", plainLoaded, "op://a/b/c", "cannot carry a resolve"},
		{"empty value", empty, "op://a/b/c", "empty value"},
		{"unknown action", odd, "op://a/b/c", "not a resolve result"},
		{"transport error", broken, "op://a/b/c", "pipe closed"},
	}
	for _, tc := range cases {
		value, err := tc.m.ResolveSecret(context.Background(), tc.ref)
		var backendErr *SecretBackendError
		if value != "" || !errors.As(err, &backendErr) {
			t.Errorf("%s: %q, %v", tc.name, value, err)
			continue
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "credential_missing: ") || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %q, want credential_missing containing %q", tc.name, msg, tc.want)
		}
		// AGENTS.md: a recovery instruction must survive redact.Text.
		if got := redact.Text(msg); got != msg {
			t.Errorf("%s: redaction changed the recovery:\n  %s\n  %s", tc.name, msg, got)
		}
	}
}

// A resolve that arrives while its backend is still loading waits for it.
func TestResolveSecretWaitsForALoadingBackend(t *testing.T) {
	process := newBackendProcess(SDKCommandResult{Action: plugin.ResolveActionValue, Content: backendValue})
	m := newTestManager(t, nil, fakeLauncher{process: process}, "test")
	m.RegisterInstalled(backendPlugin("onepassword", "op"))

	// Hold the load open as P5-d's load does while Init runs.
	done := make(chan struct{})
	m.mu.Lock()
	m.loading["onepassword"] = done
	m.mu.Unlock()
	go func() {
		time.Sleep(30 * time.Millisecond)
		m.mu.Lock()
		delete(m.loading, "onepassword")
		m.mu.Unlock()
		if err := m.Load(context.Background(), "onepassword"); err != nil {
			t.Error(err)
		}
		close(done)
	}()
	value, err := m.ResolveSecret(context.Background(), "op://a/b/c")
	if err != nil || value != backendValue {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
}

func TestResolveSecretStopsWaitingAtItsBound(t *testing.T) {
	saved := secretBackendWait
	secretBackendWait = 20 * time.Millisecond
	t.Cleanup(func() { secretBackendWait = saved })
	m := newTestManager(t, nil, fakeLauncher{}, "test")
	m.RegisterInstalled(backendPlugin("onepassword", "op"))
	m.mu.Lock()
	m.loading["onepassword"] = make(chan struct{})
	m.mu.Unlock()
	_, err := m.ResolveSecret(context.Background(), "op://a/b/c")
	if err == nil || !strings.Contains(err.Error(), "still loading") {
		t.Fatalf("err = %v", err)
	}
}

// A backend's own credential comes from the core chain; every other
// plugin's from the full chain.
func TestABackendsCredentialComesFromTheCoreChain(t *testing.T) {
	full := &fakeResolver{values: map[string]string{"onepassword/token": "from-full", "docker/token": "from-full"}}
	core := &fakeResolver{values: map[string]string{"onepassword/token": "from-core"}}
	m := newTestManager(t, nil, fakeLauncher{}, "test", WithSecretResolver(full), WithCoreSecretResolver(core))

	backend := backendPlugin("onepassword", "op")
	if got := m.secretsFor(backend); got != core {
		t.Fatal("a secret backend resolves its credential through the full chain")
	}
	ordinary := validInstalledPlugin()
	if got := m.secretsFor(ordinary); got != full {
		t.Fatal("an ordinary plugin resolves through the core chain")
	}
	resolved := resolvePluginSecrets(context.Background(), m.secretsFor(backend), backend)
	if resolved.Config["token"] != "from-core" || len(full.lookups) != 0 {
		t.Fatalf("resolved %v; full chain lookups %v", resolved.Config, full.lookups)
	}
}

func TestReviewShowsTheSecretBackend(t *testing.T) {
	p := backendPlugin("onepassword", "op")
	review := BuildReview(&Staged{Spec: p.Spec, Digest: "sha256:x"}, "dir", OriginInstalled)
	text := review.Render()
	for _, want := range []string{"SECRET BACKEND", "every secret resolved through op://", "onepassword/token", "never from another vault"} {
		if !strings.Contains(text, want) {
			t.Errorf("review lacks %q:\n%s", want, text)
		}
	}

	plain := BuildReview(&Staged{Spec: validInstalledPlugin().Spec, Digest: "sha256:x"}, "dir", OriginInstalled)
	if strings.Contains(plain.Render(), "SECRET BACKEND") {
		t.Fatal("an ordinary plugin's review shows a backend")
	}
	if diff := strings.Join(Diff(plain, review), "\n"); !strings.Contains(diff, "+ secret backend for op://") {
		t.Errorf("diff = %s", diff)
	}
	other := backendPlugin("onepassword", "op2")
	changed := BuildReview(&Staged{Spec: other.Spec, Digest: "sha256:x"}, "dir", OriginInstalled)
	if diff := strings.Join(Diff(review, changed), "\n"); !strings.Contains(diff, "~ secret backend scheme op:// -> op2://") {
		t.Errorf("diff = %s", diff)
	}
}

// hangingBackend never answers a resolve until its context ends.
type hangingBackend struct{ fakeProcess }

func (h *hangingBackend) Command(ctx context.Context, _ SDKCommandRequest) (SDKCommandResult, error) {
	<-ctx.Done()
	return SDKCommandResult{}, ctx.Err()
}
func (h *hangingBackend) Kill() {}

// A resolve has its own short deadline, not the two-minute call deadline:
// a hung backend holds a dependent plugin's load for seconds, not minutes.
func TestAResolveHasItsOwnDeadline(t *testing.T) {
	if DefaultLimits.Resolve != 10*time.Second || DefaultLimits.Resolve >= DefaultLimits.Call {
		t.Fatalf("default resolve deadline %s", DefaultLimits.Resolve)
	}
	m := loadedBackend(t, &hangingBackend{fakeProcess: fakeProcess{initResult: SDKInitResult{CapabilityContract: 1, ID: "onepassword", Version: "dev", Protocol: SDKProtocolVersion}}})
	m.mu.Lock()
	m.running["onepassword"].limits.Resolve = 40 * time.Millisecond
	m.mu.Unlock()
	start := time.Now()
	_, err := m.ResolveSecret(context.Background(), "op://a/b/c")
	if err == nil || !strings.Contains(err.Error(), "did not answer resolve within 40ms") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the resolve took %s", elapsed)
	}
}

func TestResolveTimeoutIsALimitClampedLikeTheOthers(t *testing.T) {
	l, warnings := ClampLimits(&LimitSettings{ResolveTimeout: 5 * time.Second})
	if l.ResolveTimeout() != 5*time.Second || len(warnings) != 0 {
		t.Fatalf("set: %s %v", l.ResolveTimeout(), warnings)
	}
	l, warnings = ClampLimits(&LimitSettings{ResolveTimeout: time.Hour})
	if l.ResolveTimeout() != MaxLimits.Resolve || len(warnings) != 1 || !strings.Contains(warnings[0], "limits.resolve_timeout") {
		t.Fatalf("clamp: %s %v", l.ResolveTimeout(), warnings)
	}
	if l, _ := ClampLimits(nil); l.ResolveTimeout() != DefaultLimits.Resolve {
		t.Fatalf("default: %s", l.ResolveTimeout())
	}
}

// With no core chain configured, a backend's own credential resolves
// through nothing, rather than falling back to the chain that routes to
// backends.
func TestABackendWithNoCoreChainFailsClosed(t *testing.T) {
	full := &fakeResolver{values: map[string]string{"onepassword/token": "from-full"}}
	m := newTestManager(t, nil, fakeLauncher{}, "test", WithSecretResolver(full))
	backend := backendPlugin("onepassword", "op")
	resolved := resolvePluginSecrets(context.Background(), m.secretsFor(backend), backend)
	if resolved.Config["token"] != "" || len(full.lookups) != 0 {
		t.Fatalf("a backend reached the full chain: %v %v", resolved.Config, full.lookups)
	}
	if len(resolved.Problems) == 0 || !strings.Contains(strings.Join(resolved.Problems, " "), "no core credential chain") {
		t.Fatalf("problems = %v", resolved.Problems)
	}
}

// A backend in its restart backoff is being restarted: the caller is told
// to retry, not to load it.
func TestAResolveDuringARestartSaysRetry(t *testing.T) {
	m := newTestManager(t, nil, fakeLauncher{}, "test")
	m.RegisterInstalled(backendPlugin("onepassword", "op"))
	m.sup.mu.Lock()
	m.sup.pending["onepassword"] = true
	m.sup.mu.Unlock()
	_, err := m.ResolveSecret(context.Background(), "op://a/b/c")
	if err == nil || !strings.Contains(err.Error(), "Cerberus is restarting it; retry shortly") || strings.Contains(err.Error(), "managed load") {
		t.Fatalf("err = %v", err)
	}
}

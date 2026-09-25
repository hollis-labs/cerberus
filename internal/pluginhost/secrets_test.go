package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// fakeResolver is the host's secret store as the plugin lane sees it: a
// connector-scoped lookup that returns empty rather than erroring when a
// credential is simply absent, exactly like the keychain provider.
type fakeResolver struct {
	mu      sync.Mutex
	values  map[string]string
	err     error
	lookups []string
}

func (f *fakeResolver) Get(_ context.Context, service, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, service+"/"+key)
	if f.err != nil {
		return "", f.err
	}
	return f.values[service+"/"+key], nil
}

type recordingProcess struct {
	initParams SDKInitParams
	callErr    error
}

func (p *recordingProcess) Init(_ context.Context, params SDKInitParams) (SDKInitResult, error) {
	p.initParams = params
	return SDKInitResult{ID: "contextforge", Version: "dev", Protocol: SDKProtocolVersion}, nil
}
func (p *recordingProcess) Load(context.Context) (SDKLoadResult, error) {
	return SDKLoadResult{}, nil
}
func (p *recordingProcess) Unload(context.Context) error { return nil }
func (p *recordingProcess) Health(context.Context) (SDKHealthResult, error) {
	return SDKHealthResult{OK: true}, nil
}
func (p *recordingProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	if p.callErr != nil {
		return SDKMCPCallResult{}, p.callErr
	}
	return SDKMCPCallResult{Content: []byte(`"ok"`)}, nil
}
func (p *recordingProcess) Close() error { return nil }

// secretDeclaringPlugin mirrors the ContextForge manifest: one required
// credential, plus operations that do and do not need it.
func secretDeclaringPlugin() InstalledPlugin {
	plugin := validInstalledPlugin()
	plugin.ID = "contextforge"
	plugin.Manifest.ID = "contextforge"
	plugin.Manifest.Config = contract.ConfigSchema{
		Secrets: []contract.SecretRequirement{
			{Name: "token", Env: "CONTEXTFORGE_TOKEN", Required: true},
			{Name: "fallback_token", Env: "CONTEXTFORGE_FALLBACK"},
		},
	}
	plugin.Manifest.Operations = []contract.ManifestOperation{
		{Name: "get_health", InputSchema: contract.ObjectSchema(map[string]any{})},
		{Name: "list_gateways", InputSchema: contract.ObjectSchema(map[string]any{})},
	}
	return plugin
}

func loadWithResolver(t *testing.T, resolver SecretResolver, process Process, plugin InstalledPlugin) (*Manager, *[]string) {
	t.Helper()
	var warnings []string
	manager := NewManager(nil, fakeLauncher{process: process}, "test",
		WithSecretResolver(resolver),
		WithLoadWarning(func(line string) { warnings = append(warnings, line) }),
	)
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return manager, &warnings
}

// The contract gap WP-7 closes: docs/secrets.md promises a resource names a
// credential and the host resolves it. Before this the host handed plugins an
// unconditionally empty config and every plugin reimplemented the lookup.
func TestManagerResolvesDeclaredSecretsIntoInitConfig(t *testing.T) {
	resolver := &fakeResolver{values: map[string]string{
		"contextforge/token":          "jwt-value",
		"contextforge/fallback_token": "second-value",
	}}
	process := &recordingProcess{}
	manager, warnings := loadWithResolver(t, resolver, process, secretDeclaringPlugin())
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	if got := process.initParams.Config["token"]; got != "jwt-value" {
		t.Fatalf("Config[token] = %q, want the resolved value", got)
	}
	if got := process.initParams.Config["fallback_token"]; got != "second-value" {
		t.Fatalf("Config[fallback_token] = %q, want the resolved value", got)
	}
	if len(process.initParams.Config) != 2 {
		t.Fatalf("Config = %v, want only the two declared secrets", keysOf(process.initParams.Config))
	}
	if len(*warnings) != 0 {
		t.Fatalf("warnings = %v, want none when every secret resolved", *warnings)
	}
	if len(manager.MissingSecrets("contextforge")) != 0 {
		t.Fatalf("MissingSecrets = %v, want none", manager.MissingSecrets("contextforge"))
	}

	// Scoped to the plugin's own id, so `connector-secrets.yaml` and
	// CERBERUS_<ID>_<NAME> mean the same thing either side of the boundary.
	want := []string{"contextforge/token", "contextforge/fallback_token"}
	if strings.Join(resolver.lookups, ",") != strings.Join(want, ",") {
		t.Fatalf("lookups = %v, want %v", resolver.lookups, want)
	}
}

// A plugin receives only what its own manifest declares — never the store.
func TestManagerPassesOnlyDeclaredSecrets(t *testing.T) {
	resolver := &fakeResolver{values: map[string]string{
		"contextforge/token":     "jwt-value",
		"digitalocean/api_token": "someone-elses-token",
		"cloudflare/api_token":   "another-token",
	}}
	plugin := secretDeclaringPlugin()
	plugin.Manifest.Config.Secrets = plugin.Manifest.Config.Secrets[:1]
	process := &recordingProcess{}
	manager, _ := loadWithResolver(t, resolver, process, plugin)
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	if len(process.initParams.Config) != 1 {
		t.Fatalf("Config = %v, want only the declared secret", keysOf(process.initParams.Config))
	}
	for _, value := range process.initParams.Config {
		if strings.Contains(value, "someone-elses") || strings.Contains(value, "another-token") {
			t.Fatal("a plugin was handed a credential belonging to another connector")
		}
	}
}

// An optional component must not be able to take the host down: the plugin
// loads, and the operation that needed the credential is the thing that fails.
func TestManagerLoadsWithoutRequiredSecret(t *testing.T) {
	process := &recordingProcess{callErr: errors.New("401 Unauthorized")}
	manager, warnings := loadWithResolver(t, &fakeResolver{}, process, secretDeclaringPlugin())
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	if _, ok := process.initParams.Config["token"]; ok {
		t.Fatal("an unresolved secret must be absent, not present and empty")
	}
	missing := manager.MissingSecrets("contextforge")
	if len(missing) != 1 || missing[0] != "token" {
		t.Fatalf("MissingSecrets = %v, want [token] — the required one only", missing)
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "token") {
		t.Fatalf("warnings = %v, want one naming the missing credential", *warnings)
	}

	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{
		Connector: "contextforge",
		Operation: "list_gateways",
	})
	var missingErr *MissingSecretsError
	if !errors.As(err, &missingErr) {
		t.Fatalf("ExecuteOperation error = %v, want a MissingSecretsError", err)
	}
	for _, want := range []string{"CERBERUS_CONTEXTFORGE_TOKEN", "connector-secrets.yaml", "401 Unauthorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err.Error(), want)
		}
	}

	// Every operator-facing error runs through redact.Text. Phrased as
	// "credential token: set FOO" it reads as an assignment and comes back
	// "credential token: [REDACTED] FOO" — the safety net destroying the
	// instruction. Guidance that cannot survive redaction is not guidance.
	redacted := redact.Text(err.Error())
	for _, want := range []string{"CERBERUS_CONTEXTFORGE_TOKEN", "keychain://", "connector-secrets.yaml"} {
		if !strings.Contains(redacted, want) {
			t.Fatalf("redaction mangled the guidance, leaving %q without %q", redacted, want)
		}
	}
}

// ContextForge's get_health is open, and is the fastest way to tell a down
// tunnel from a down gateway. A missing token must not pre-empt it.
func TestManagerMissingSecretDoesNotBlockOperationsThatSucceed(t *testing.T) {
	process := &recordingProcess{}
	manager, _ := loadWithResolver(t, &fakeResolver{}, process, secretDeclaringPlugin())
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	if _, err := manager.ExecuteOperation(context.Background(), OperationArgs{
		Connector: "contextforge",
		Operation: "get_health",
	}); err != nil {
		t.Fatalf("an operation that does not need the credential failed: %v", err)
	}
}

// A store that refuses to unlock is reported, redacted, and is not fatal.
func TestManagerSecretResolverFailureIsReportedNotFatal(t *testing.T) {
	resolver := &fakeResolver{err: fmt.Errorf("keychain get contextforge/token: api_key=sk-ant-0123456789abcdef0123")}
	process := &recordingProcess{}
	manager, warnings := loadWithResolver(t, resolver, process, secretDeclaringPlugin())
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()

	joined := strings.Join(*warnings, "\n")
	if !strings.Contains(joined, "token") {
		t.Fatalf("warnings = %v, want the failing credential named", *warnings)
	}
	if strings.Contains(joined, "sk-ant-0123456789abcdef0123") {
		t.Fatalf("warnings leaked a credential: %v", *warnings)
	}
}

// No resolver configured is the pre-WP-7 behavior, and must stay harmless.
func TestManagerWithoutSecretResolverStillLoads(t *testing.T) {
	process := &recordingProcess{}
	manager := NewManager(nil, fakeLauncher{process: process}, "test")
	manager.RegisterInstalled(secretDeclaringPlugin())
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = manager.Unload(context.Background(), "contextforge") }()
	if len(process.initParams.Config) != 0 {
		t.Fatalf("Config = %v, want empty with no resolver", keysOf(process.initParams.Config))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

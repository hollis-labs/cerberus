package cerbapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/secretref"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

func claims(scheme string) func(*pluginhost.PluginYAML) {
	return func(spec *pluginhost.PluginYAML) {
		spec.Cerberus.SecretBackend = &plugin.SecretBackend{Scheme: scheme}
	}
}

// A secret backend restores before the plugins that resolve through it.
func TestRestorePhasePutsSecretBackendsFirst(t *testing.T) {
	backend := pluginhost.InstalledPlugin{Path: t.TempDir(), Spec: pluginhost.PluginYAML{Cerberus: plugin.CerberusPluginBlock{SecretBackend: &plugin.SecretBackend{Scheme: "op"}}}}
	if restorePhase(backend) != 0 || restorePhase(pluginhost.InstalledPlugin{}) != 1 {
		t.Fatalf("phases: backend %d, other %d", restorePhase(backend), restorePhase(pluginhost.InstalledPlugin{}))
	}
}

// One claimant per scheme: the second plugin claiming op:// is not
// registered, and the refusal names the holder and the recovery. The binder
// sees the service before any restore.
func TestASecondClaimOnASchemeIsRefused(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	st := pluginConnectorPersistedState{Entries: []pluginConnectorPersistedEntry{
		{PluginDir: reviewablePluginDir(t, "onepassword", claims("op"))},
		{PluginDir: reviewablePluginDir(t, "impostor", claims("op"))},
	}}
	if err := writePluginConnectorState(statePath, st); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	var bound secretref.SchemeRouter
	managed, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", &warnings, statePath,
		WithSecretBackendBinder(func(r secretref.SchemeRouter) { bound = r }))
	if err != nil {
		t.Fatal(err)
	}
	if bound != managed {
		t.Fatal("the service was not bound as the secret backend router")
	}
	if !managed.Claims("op") || managed.Claims("keeper") {
		t.Fatalf("claims: op %v keeper %v", managed.Claims("op"), managed.Claims("keeper"))
	}
	if _, ok := managed.manager.Installed("impostor"); ok {
		t.Fatal("a second claimant on op:// was registered")
	}
	w := warnings.String()
	if !strings.Contains(w, `which plugin "onepassword" already claims`) || !strings.Contains(w, "managed uninstall onepassword") {
		t.Fatalf("warning = %s", w)
	}
}

// run-secrets' backends: the state file's plugins registered, none loaded,
// the one a reference needs loaded on demand in this process and resolved
// through, the state file untouched, and everything stopped by Close.
func TestProcessSecretBackendsLoadOnDemandAndStop(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	sink := audit.NewMemory()
	reviewInstall(t, sink, statePath, reviewablePluginDir(t, "docker", claims("op")))
	before, err := os.ReadFile(statePath) //nolint:gosec // the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}

	backends, err := NewProcessSecretBackends(sink, "test", nil, statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !backends.Claims("op") || backends.svc.manager.Loaded("docker") {
		t.Fatalf("claims %v, loaded %v; want claimed and not loaded", backends.Claims("op"), backends.svc.manager.Loaded("docker"))
	}
	if got := backends.Backend("op"); got != "docker@1.0.0" {
		t.Fatalf("Backend = %q", got)
	}

	value, err := backends.ResolveSecret(t.Context(), "op://Deploy/Database/password")
	if err != nil || value != "resolved-op:--Deploy-Database-password" {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
	var loadRecord audit.Record
	for _, r := range sink.Records() {
		if r.Kind == audit.KindOutcome && r.Operation == "load" && r.Principal.Via == "run_secrets" {
			loadRecord = r
		}
	}
	if loadRecord.Principal.Surface != "run_secrets" || !strings.Contains(loadRecord.Reason, "secret backend loaded by run-secrets") {
		t.Fatalf("the one-shot load was not recorded as run-secrets: %+v", loadRecord)
	}

	backends.Close(t.Context())
	if backends.svc.manager.Loaded("docker") {
		t.Fatal("Close left the backend running")
	}
	after, err := os.ReadFile(statePath) //nolint:gosec // the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("run-secrets' backends wrote the daemon's state file")
	}
}

// A backend whose install review is pending is not handed secrets by a
// managed service.
func TestProcessSecretBackendsRefuseAnUnreviewedBackend(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	st := pluginConnectorPersistedState{Entries: []pluginConnectorPersistedEntry{
		{PluginDir: reviewablePluginDir(t, "docker", claims("op"))},
	}}
	if err := writePluginConnectorState(statePath, st); err != nil {
		t.Fatal(err)
	}
	backends, err := NewProcessSecretBackends(audit.NewMemory(), "test", nil, statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := backends.ResolveSecret(t.Context(), "op://a/b/c")
	if value != "" || err == nil || !strings.Contains(err.Error(), "install review is pending") || !strings.Contains(err.Error(), "managed review docker") {
		t.Fatalf("ResolveSecret = %q, %v", value, err)
	}
	if backends.svc.manager.Loaded("docker") {
		t.Fatal("an unreviewed backend was started")
	}
}

// The outcome of a gated call names where each credential it resolved came
// from: names and sources, never values.
func TestOutcomesCarryCredentialSources(t *testing.T) {
	sink := audit.NewMemory()
	call, err := beginAudit(t.Context(), sink, slog.Default(), restoreSpec(pluginhost.InstalledPlugin{Path: t.TempDir(), ID: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := call.withSources(t.Context())
	t.Setenv("CERBERUS_GITHUB_TOKEN", "ghp_notreallyatoken00000000000000000000")
	provider := secrets.NewReferenceProvider(nil, "")
	if _, err := provider.Get(ctx, "github", "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Get(ctx, "cloudflare", "api_token"); err != nil {
		t.Fatal(err)
	}
	call.finish(nil)
	var outcome audit.Record
	for _, r := range sink.Records() {
		if r.Kind == audit.KindOutcome {
			outcome = r
		}
	}
	want := map[string]string{"github/token": "env", "cloudflare/api_token": "missing"}
	if len(outcome.CredentialSources) != len(want) {
		t.Fatalf("credential_sources = %v", outcome.CredentialSources)
	}
	for k, v := range want {
		if outcome.CredentialSources[k] != v {
			t.Fatalf("credential_sources = %v, want %v", outcome.CredentialSources, want)
		}
	}
	data, _ := json.Marshal(outcome)
	if strings.Contains(string(data), "ghp_notreally") {
		t.Fatal("a credential value reached the audit record")
	}
}

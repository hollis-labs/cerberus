package cerbapi

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

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
	backend := pluginhost.InstalledPlugin{Spec: pluginhost.PluginYAML{Cerberus: plugin.CerberusPluginBlock{SecretBackend: &plugin.SecretBackend{Scheme: "op"}}}}
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

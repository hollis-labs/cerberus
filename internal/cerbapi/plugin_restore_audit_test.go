package cerbapi

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// A daemon restart reloads its plugins, and each load is recorded as
// automation with its reason and whether its bundle was checked — a restart
// is exactly when a pending plugin starts code nobody reviewed.
func TestRestoreRecordsEachPluginLoad(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	reviewInstall(t, audit.NewMemory(), statePath, reviewablePluginDir(t, "docker", nil))
	st, _ := readPluginConnectorState(statePath)
	st.Entries[0].Loaded = true
	pending := reviewablePluginDir(t, "widget", nil)
	st.Entries = append(st.Entries, pluginConnectorPersistedEntry{PluginDir: pending, Loaded: true})
	if err := writePluginConnectorState(statePath, st); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	managed, err := NewManagedPluginConnectorService(sink, "test", nil, statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []string{"docker", "widget"} {
			_, _ = managed.Unload(t.Context(), id)
		}
	})
	loads := map[string]audit.Record{}
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindOutcome && rec.Connector == "plugin" && rec.Operation == "load" {
			loads[rec.Target.Fields["id"]] = rec
		}
	}
	docker, widget := loads["docker"], loads["widget"]
	if docker.Principal.Kind != audit.PrincipalAutomation || docker.Principal.Via != "daemon_start" || docker.Principal.SelfReported ||
		!strings.Contains(docker.Reason, "restore at daemon start; bundle sha256:") || docker.OutcomeCode != audit.OutcomeOK || docker.PluginEntrypointSHA256 == "" {
		t.Fatalf("reviewed restore: %+v", docker)
	}
	if !strings.Contains(widget.Reason, "unchecked: its review is pending") || widget.OutcomeCode != audit.OutcomeOK {
		t.Fatalf("pending restore: %+v", widget)
	}
}

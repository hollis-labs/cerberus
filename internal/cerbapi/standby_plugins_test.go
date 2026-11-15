package cerbapi

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// A daemon built with deferred plugin loading starts none; standby suspends
// every loaded plugin without writing the state file or the audit log, the
// supervisor does not bring them back, and RestorePlugins loads the same set.
func TestStandbySuspendsPluginsAndRestoresTheSameSet(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	reviewInstall(t, audit.NewMemory(), statePath, reviewablePluginDir(t, "docker", nil))
	st, _ := readPluginConnectorState(statePath)
	st.Entries[0].Loaded = true
	if err := writePluginConnectorState(statePath, st); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	managed, err := NewManagedPluginConnectorService(sink, "test", nil, statePath, WithDeferredPluginLoad())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = managed.Unload(t.Context(), "docker") })
	if loaded := managed.LoadedPlugins(); len(loaded) != 0 || len(sink.Records()) != 0 {
		t.Fatalf("deferred load started %v and recorded %d", loaded, len(sink.Records()))
	}

	if err = managed.RestorePlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loaded := managed.LoadedPlugins(); len(loaded) != 1 || loaded[0] != "docker" {
		t.Fatalf("restore loaded %v", loaded)
	}
	stateBefore, _ := os.ReadFile(statePath) //nolint:gosec // the test's own temp file
	recordsBefore := len(sink.Records())

	stopped, err := managed.SuspendPlugins(t.Context())
	if err != nil || len(stopped) != 1 || stopped[0] != "docker" {
		t.Fatalf("suspend = %v, %v", stopped, err)
	}
	if loaded := managed.LoadedPlugins(); len(loaded) != 0 {
		t.Fatalf("still loaded after suspend: %v", loaded)
	}
	time.Sleep(1500 * time.Millisecond) // past the supervisor's first restart backoff
	if loaded := managed.LoadedPlugins(); len(loaded) != 0 {
		t.Fatalf("the supervisor restarted a suspended plugin: %v", loaded)
	}
	stateAfter, _ := os.ReadFile(statePath) //nolint:gosec // the test's own temp file
	if !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("suspend wrote the plugin state file")
	}
	if len(sink.Records()) != recordsBefore {
		t.Fatalf("suspend wrote %d audit records", len(sink.Records())-recordsBefore)
	}

	if err = managed.RestorePlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loaded := managed.LoadedPlugins(); len(loaded) != 1 || loaded[0] != "docker" {
		t.Fatalf("restore after suspend loaded %v", loaded)
	}
	// Restoring what is already loaded loads nothing twice.
	recordsBefore = len(sink.Records())
	if err = managed.RestorePlugins(t.Context()); err != nil || len(sink.Records()) != recordsBefore {
		t.Fatalf("a second restore did something: %v, %d records", err, len(sink.Records())-recordsBefore)
	}
}

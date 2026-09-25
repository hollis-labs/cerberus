package cerbapi

import (
	"context"
	"errors"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// A plugin may not claim a connector this binary serves itself. WP-0 added a
// fallback so an installed-but-unloaded plugin stopped disabling the built-in
// it shadowed; this refuses the shadow in the first place.
func TestManagedPluginInstallRefusesABuiltInID(t *testing.T) {
	managed, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard,
		filepath.Join(t.TempDir(), "state.json"),
		WithManagedPluginReservedIDs("local", "ssh", "docker", "github"))
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}

	_, err = managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	})
	var reserved *pluginhost.ReservedIDError
	if !errors.As(err, &reserved) {
		t.Fatalf("Install error = %v, want a ReservedIDError", err)
	}
	if managed.Installed("ssh") {
		t.Fatal("a refused plugin must not reach the inventory")
	}
	if !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("error %q should name the colliding id", err.Error())
	}
}

// The refusal is the guard doing its job, not installs being broken: the same
// plugin installs when nothing reserves the id.
func TestManagedPluginInstallAllowsTheSameIDWhenNothingReservesIt(t *testing.T) {
	managed, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard,
		filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}
	if !managed.Installed("ssh") {
		t.Fatal("plugin should be installed")
	}
}

// An inventory registered before the guard existed can hold a shadowing plugin.
// Restore must skip it with a warning and keep the registration — a plugin is
// optional by definition and must not take the daemon down, and silently
// dropping the entry would lose the operator's record of it.
func TestManagedPluginRestoreSkipsAReservedIDWithoutFailing(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	pluginDir := writeTestPluginDir(t, "ssh")

	// Installed before the guard: no reserved ids.
	managed, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, statePath)
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
	}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}

	var warnings strings.Builder
	restored, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", &warnings, statePath,
		WithManagedPluginReservedIDs("ssh"))
	if err != nil {
		t.Fatalf("restore failed instead of skipping a now-refused plugin: %v", err)
	}
	if restored.Installed("ssh") {
		t.Fatal("a plugin shadowing a built-in must not be restored into the inventory")
	}
	if !strings.Contains(warnings.String(), "skipping plugin") {
		t.Fatalf("the skip was silent; warnings = %q", warnings.String())
	}

	if persistErr := restored.persist(); persistErr != nil {
		t.Fatalf("persist: %v", persistErr)
	}
	state, err := readPluginConnectorState(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(state.Entries) != 1 || state.Entries[0].PluginDir != pluginDir {
		t.Fatalf("the registration was dropped; entries = %+v", state.Entries)
	}
}

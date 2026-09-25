package cerbapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"gopkg.in/yaml.v3"
)

// installForTest registers a plugin directory the way an entry from before
// install review is registered: unreviewed, loading unchecked. Tests that
// exercise what a plugin does, rather than how it was installed, set up with
// it; the review itself is tested through PluginReviewer.
func (s *ManagedPluginConnectorService) installForTest(_ context.Context, args PluginConnectorHealthArgs) (ManagedPluginConnectorState, error) {
	entry := pluginConnectorPersistedEntry{PluginDir: args.PluginDir, Options: args.InstallOptions()}
	installed, err := s.register(entry)
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	if err := updatePluginConnectorState(s.statePath, func(st *pluginConnectorPersistedState) error {
		if st.findEntry(installed.ID) < 0 {
			entry.ID = installed.ID
			st.Entries = append(st.Entries, entry)
		}
		return nil
	}); err != nil {
		return ManagedPluginConnectorState{}, err
	}
	return managedState(installed, false), nil
}

// reviewablePluginDir is helperPluginDir with an entrypoint a bundle can
// hold: a script that execs the test binary, since a bundle refuses
// symlinks. ops replaces the default operation list when given.
func reviewablePluginDir(t *testing.T, id string, mutate func(*pluginhost.PluginYAML)) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", `'\''`) + "' -test.run=TestPluginConnectorSDKHelperProcess \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), []byte(script), 0o755); err != nil { //nolint:gosec // test plugin entrypoint
		t.Fatal(err)
	}
	spec := pluginhost.PluginYAMLFromManifest(contract.Manifest{
		APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: id, Version: "1.0.0",
		ResourceTypes: []string{"container"},
		Operations: []contract.ManifestOperation{
			{Name: "logs", Effect: contract.EffectReadSensitive, Output: contract.OutputFreeText, InputSchema: contract.ObjectSchema(map[string]any{"container": contract.StringSchema("Container.")})},
		},
	}, pluginhost.Entrypoint{Command: "bin/plugin"})
	if mutate != nil {
		mutate(&spec)
	}
	writePluginYAML(t, dir, spec)
	return dir
}

func writePluginYAML(t *testing.T, dir string, spec pluginhost.PluginYAML) {
	t.Helper()
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginhost.PluginYAMLFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// reviewInstall runs an install review and accepts it, as the operator
// typing the plugin id would.
func reviewInstall(t *testing.T, sink audit.Sink, statePath, dir string) ManagedPluginConnectorState {
	t.Helper()
	ctx := WithCallerSurface(context.Background(), SurfaceInProcess)
	r := NewPluginReviewer(sink, statePath)
	pending, err := r.PrepareInstall(ctx, dir, false)
	if err != nil {
		t.Fatalf("PrepareInstall: %v", err)
	}
	state, err := r.Accept(ctx, pending, pending.Review.ID)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return state
}

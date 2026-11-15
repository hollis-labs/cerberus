package admin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestPluginStateForTheOldBuild writes plugin-connectors.json as this build writes it and reads it
// back. With CERBERUS_COMPAT_OUT set it leaves the file there, for main's own reader to read (the
// compatibility check of slice 8b: old reads what new wrote; the procedure is in the PR).
func TestPluginStateForTheOldBuild(t *testing.T) {
	dir := os.Getenv("CERBERUS_COMPAT_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(dir, "plugin-connectors.json")
	want := persistedState{Version: 2, Entries: []persistedEntry{
		{ID: "echo", PluginDir: "/opt/cerberus/plugins/echo", Source: "/src/echo", Loaded: true,
			BundleDigest: "sha256:0f0e", AcceptedAt: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)},
		{ID: "notes", PluginDir: "/opt/cerberus/plugins/notes", Loaded: false},
	}}
	if err := writePluginState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readPluginState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

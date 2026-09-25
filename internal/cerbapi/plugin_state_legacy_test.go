package cerbapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// A state file written before P0-4 carries a "trust" object with the retired
// signing fields. It must still load: the fields are ignored, the entrypoint
// is fingerprinted by the host rather than taken from archive_sha256, and the
// next write drops the legacy object.
func TestLegacyPluginStateStillLoads(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	pluginDir := helperPluginDir(t)
	legacy := `{
  "entries": [
    {
      "plugin_dir": "` + pluginDir + `",
      "trust": {
        "dev_mode": false,
        "catalog_signed": true,
        "archive_signed": true,
        "archive_sha256": "deadbeef"
      },
      "loaded": true
    }
  ]
}`
	if err := os.WriteFile(statePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := mustManagedPluginService(t, statePath)
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].Loaded {
		t.Fatalf("legacy record did not restore and load: %#v", list)
	}
	if list[0].Origin != string(pluginhost.OriginInstalled) {
		t.Fatalf("Origin = %q, want installed", list[0].Origin)
	}
	if list[0].EntrypointSHA256 == "" || list[0].EntrypointSHA256 == "deadbeef" {
		t.Fatalf("EntrypointSHA256 = %q, want the host-computed fingerprint", list[0].EntrypointSHA256)
	}

	// Any write goes out in the new shape, with no signing vocabulary.
	if _, err = svc.Unload(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	data, err := os.ReadFile(statePath) //nolint:gosec // the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"trust", "signed", "archive_sha256"} {
		if strings.Contains(string(data), word) {
			t.Errorf("rewritten state still contains %q:\n%s", word, data)
		}
	}
}

// dev_mode is the one legacy field that still means something, and a
// development install is a restriction, so it must not be dropped: not from
// an old state file, and not from an older CLI's install request.
func TestLegacyDevModeIsHonored(t *testing.T) {
	var args PluginConnectorHealthArgs
	if err := json.Unmarshal([]byte(`{"plugin_dir":"/p","trust":{"dev_mode":true,"catalog_signed":true}}`), &args); err != nil {
		t.Fatal(err)
	}
	if !args.InstallOptions().DevMode {
		t.Fatal("legacy trust.dev_mode on an install request was dropped")
	}

	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	pluginDir := helperPluginDir(t)
	legacy := `{"entries":[{"plugin_dir":"` + pluginDir + `","trust":{"dev_mode":true},"loaded":true}]}`
	if err := os.WriteFile(statePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := mustManagedPluginService(t, statePath)
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if pluginhost.DevModeEnabled {
		if len(list) != 1 || list[0].Origin != string(pluginhost.OriginDev) {
			t.Fatalf("legacy dev record restored as %#v, want origin dev", list)
		}
		return
	}
	// A release build refuses a development install, so the record is kept
	// but not restored as an ordinary install.
	if len(list) != 0 {
		t.Fatalf("legacy dev record restored without devmode: %#v", list)
	}
}

// Package admintest installs a synthetic third-party plugin into an adopted root, the way the
// old build's install review left one: a reviewed bundle in plugins/<id>/<digest>/ and its
// entry in plugin-connectors.json (format version 2) with the accepted bundle digest and
// review. Tests of the admin lane and the process tests use it; nothing else does.
package admintest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// BuildEcho builds the synthetic plugin (internal/admin/testdata/echoplugin, which uses the
// public plugin SDK only) into dir and returns the binary's path.
func BuildEcho(dir string) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("admintest: cannot find the plugin source")
	}
	src := filepath.Join(filepath.Dir(file), "..", "testdata", "echoplugin")
	out := filepath.Join(dir, "echo-plugin")
	cmd := exec.Command("go", "build", "-o", out, ".") //nolint:gosec // fixed arguments
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("admintest: build the echo plugin: %w\n%s", err, b)
	}
	return out, nil
}

// Plugin is how an echo plugin is installed.
type Plugin struct {
	// ID is the plugin and connector id (default "echo").
	ID string
	// Loaded marks the entry loaded, so a start restores it.
	Loaded bool
	// Pending leaves the entry without a review, as an install from before review did.
	Pending bool
}

// Installed is where the fixture put the plugin.
type Installed struct {
	ID     string
	Path   string
	Digest string
}

// Manifest is the echo plugin's plugin.yaml: a read (echo), an issuing write (provision,
// on the resource its id names), a secret (token), and the issuance capability.
func Manifest(id string) string {
	return strings.ReplaceAll(`schema_version: "1"
id: ID
version: 1.0.0
protocol: plugin-sdk/subprocess
runtime: subprocess
entrypoint:
  command: bin/echo-plugin
  args: [ID]
capabilities:
  - name: credential_issuance_v1
    reason: issues a login for the target it provisions
cerberus:
  connector:
    api_version: cerberus.connector/v1
    kind: Connector
    id: ID
    version: 1.0.0
    resource_types: [service]
    capabilities: {can_create: false, can_destroy: false, can_build: false, can_logs: false, can_health: true}
    config:
      secrets:
        - name: token
          description: The echo service's API token.
          env: CERBERUS_ECHO_TOKEN
    operations:
      - name: echo
        description: Echo the arguments back.
        effect: read
        input_schema:
          type: object
          additionalProperties: false
          properties:
            message: {type: string}
      - name: provision
        description: Provision a login on a target, with a credential the host issues.
        effect: write
        preview: plugin
        local_fs: writes
        target: {kind: service, from: [id]}
        issued_secrets: [runtime]
        input_schema:
          type: object
          additionalProperties: false
          required: [issuance_id]
          properties:
            issuance_id: {type: string}
            id: {type: string}
`, "ID", id)
}

// Install puts the echo plugin built at binary into adoptRoot as a reviewed bundle and records
// it in plugin-connectors.json, keeping the entries already there.
func Install(adoptRoot, binary string, p Plugin) (Installed, error) {
	if p.ID == "" {
		p.ID = "echo"
	}
	src, err := os.MkdirTemp("", "echo-src-")
	if err != nil {
		return Installed{}, err
	}
	defer func() { _ = os.RemoveAll(src) }()
	if err = WriteSource(src, binary, p.ID); err != nil {
		return Installed{}, err
	}
	store := pluginhost.Store{Root: filepath.Join(adoptRoot, "plugins")}
	staged, err := store.Stage(src)
	if err != nil {
		return Installed{}, err
	}
	review := pluginhost.BuildReview(staged, src, pluginhost.OriginInstalled)
	path, err := store.Commit(staged)
	if err != nil {
		return Installed{}, err
	}
	entry := map[string]any{"id": p.ID, "plugin_dir": path, "source": src, "options": map[string]any{}, "loaded": p.Loaded}
	if !p.Pending {
		entry["bundle_digest"] = staged.Digest
		entry["review"] = review
		entry["accepted_at"] = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	}
	statePath := filepath.Join(adoptRoot, "plugin-connectors.json")
	var state struct {
		Version int              `json:"version"`
		Entries []map[string]any `json:"entries"`
	}
	if data, readErr := os.ReadFile(statePath); readErr == nil { //nolint:gosec // the fixture's own file
		if err = json.Unmarshal(data, &state); err != nil {
			return Installed{}, err
		}
	}
	state.Version = 2
	state.Entries = append(state.Entries, entry)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return Installed{}, err
	}
	if err = os.WriteFile(statePath, data, 0o600); err != nil {
		return Installed{}, err
	}
	return Installed{ID: p.ID, Path: path, Digest: staged.Digest}, nil
}

// WriteSource writes the echo plugin built at binary, as plugin id, into dir: a plugin source
// directory an install reviews (plugin.yaml and bin/echo-plugin).
func WriteSource(dir, binary, id string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		return err
	}
	bin, err := os.ReadFile(binary) //nolint:gosec // the test's own build
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "bin", "echo-plugin"), bin, 0o700); err != nil { //nolint:gosec // an executable
		return err
	}
	return os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(Manifest(id)), 0o600)
}

// Marks are the files the echo plugin wrote under the adopted root's plugin-data (one directory
// per installation) when it loaded ("loaded") or was unloaded ("unloaded"); each holds the
// plugin process's pid.
func Marks(adoptRoot, name string) []string {
	found, _ := filepath.Glob(filepath.Join(adoptRoot, "plugin-data", "*", name))
	return found
}

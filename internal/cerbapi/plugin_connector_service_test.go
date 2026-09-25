package cerbapi

import (
	"context"
	"encoding/json"
	"github.com/hollis-labs/cerberus/internal/audit"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	sdksubprocess "github.com/hollis-labs/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

type apiTestPlugin struct{}

func (apiTestPlugin) Init(context.Context, sdksubprocess.InitParams) (sdksubprocess.InitResult, error) {
	return sdksubprocess.InitResult{
		ID:          "docker",
		Name:        "API Test Plugin",
		Version:     "test",
		Description: "api helper",
		Protocol:    sdksubprocess.ProtocolVersion,
	}, nil
}

func (apiTestPlugin) Load(context.Context) (sdksubprocess.LoadResult, error) {
	return sdksubprocess.LoadResult{}, nil
}

func (apiTestPlugin) Unload(context.Context) error { return nil }

func (apiTestPlugin) Health(context.Context) (sdksubprocess.HealthStatus, error) {
	return sdksubprocess.HealthStatus{OK: true, Message: "ready"}, nil
}

func (apiTestPlugin) MCPCallTool(_ context.Context, req sdksubprocess.MCPCallRequest) (sdksubprocess.MCPCallResult, error) {
	data, err := json.Marshal(map[string]any{
		"tool": req.ToolName,
		"args": req.Arguments,
	})
	if err != nil {
		return sdksubprocess.MCPCallResult{}, err
	}
	return sdksubprocess.MCPCallResult{Content: data}, nil
}

func TestPluginConnectorSDKHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("GO_WANT_PLUGIN_LAUNCH_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("launched"), 0o600) //nolint:gosec // test helper: the marker path is the test's own TempDir, passed through the env
	}
	if err := sdksubprocess.Serve(apiTestPlugin{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// The directory lane stays available in-process, which is what
// `cerberus connectors plugin health|exec <dir>` runs.
func TestPluginConnectorServiceHealthInProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	t.Setenv("GO_WANT_PLUGIN_LAUNCH_MARKER", marker)
	health, err := NewPluginConnectorService(audit.NewMemory(), "test", nil).Health(context.Background(), PluginConnectorHealthArgs{
		PluginDir: helperPluginDir(t),
	})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Loaded || !health.Healthy || health.ID != "docker" {
		t.Fatalf("health = %+v", health)
	}
	// The marker is how the socket test below proves nothing launched.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("launch marker not written by an in-process launch: %v", err)
	}
}

// The socket no longer runs a plugin directory: both one-shot routes answer
// 410 with the in-process alternative, and nothing is launched.
func TestSocketRefusesPluginDirRoutes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	t.Setenv("GO_WANT_PLUGIN_LAUNCH_MARKER", marker)
	socketClient := startConnectorSocket(t, NewInProcessClient())

	args := PluginConnectorExecArgs{PluginDir: helperPluginDir(t), Operation: "logs"}
	for _, path := range []string{"/plugins/connectors/health", "/plugins/connectors/operations/logs"} {
		var out map[string]any
		err := socketClient.doJSONStream(context.Background(), http.MethodPost, path, args, &out)
		if err == nil || !strings.Contains(err.Error(), "not available over the socket") {
			t.Fatalf("POST %s err = %v, want the plugin-dir refusal", path, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a plugin directory was launched over the socket")
	}
}

func TestSocketManagedExecRefusesPluginDir(t *testing.T) {
	socketClient := startConnectorSocket(t, NewInProcessClient())
	_, err := socketClient.ExecuteManagedPlugin(context.Background(), "docker", PluginConnectorExecArgs{
		PluginDir: "/tmp/anything",
		Operation: "logs",
	})
	if err == nil || !strings.Contains(err.Error(), "plugin_dir is not accepted here") {
		t.Fatalf("err = %v, want plugin_dir refusal", err)
	}
}

func TestPluginDirRefusalsSurviveRedaction(t *testing.T) {
	for _, msg := range []string{PluginDirRetired, PluginDirNotAccepted} {
		if got := redact.Text(msg); got != msg {
			t.Errorf("redact.Text changed refusal:\n got %q\nwant %q", got, msg)
		}
	}
}

func TestManagedPluginConnectorLifecycleOverSocket(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	client := NewInProcessClient(
		WithManagedPluginConnectorService(mustManagedPluginService(t, "")),
	)
	socketClient := startConnectorSocket(t, client)
	pluginDir := helperPluginDir(t)

	installed, err := socketClient.InstallManagedPlugin(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
	})
	if err != nil {
		t.Fatalf("InstallManagedPlugin: %v", err)
	}
	if installed.ID != "docker" || installed.Loaded {
		t.Fatalf("installed = %+v", installed)
	}

	list, err := socketClient.ListManagedPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListManagedPlugins: %v", err)
	}
	if len(list) != 1 || list[0].ID != "docker" {
		t.Fatalf("list = %#v", list)
	}

	loaded, err := socketClient.LoadManagedPlugin(context.Background(), "docker")
	if err != nil {
		t.Fatalf("LoadManagedPlugin: %v", err)
	}
	if !loaded.Loaded {
		t.Fatalf("loaded = %+v", loaded)
	}

	health, err := socketClient.ManagedPluginHealth(context.Background(), "docker")
	if err != nil {
		t.Fatalf("ManagedPluginHealth: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("health = %+v", health)
	}

	result, err := socketClient.ExecuteManagedPlugin(context.Background(), "docker", PluginConnectorExecArgs{
		Operation: "logs",
		Config:    map[string]any{"container": "web"},
	})
	if err != nil {
		t.Fatalf("ExecuteManagedPlugin: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["tool"] != "cerberus_docker_logs" {
		t.Fatalf("result = %#v", result)
	}

	unloaded, err := socketClient.UnloadManagedPlugin(context.Background(), "docker")
	if err != nil {
		t.Fatalf("UnloadManagedPlugin: %v", err)
	}
	if unloaded.Loaded {
		t.Fatalf("unloaded = %+v", unloaded)
	}
}

func TestManagedPluginConnectorServiceRestoresPersistedState(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	pluginDir := helperPluginDir(t)

	svc := mustManagedPluginService(t, statePath)
	installed, err := svc.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := svc.Load(context.Background(), installed.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := writePluginConnectorState(statePath, pluginConnectorPersistedState{
		Entries: []pluginConnectorPersistedEntry{
			{
				PluginDir: pluginDir,
				Loaded:    true,
			},
		},
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	restored := mustManagedPluginService(t, statePath)
	list, err := restored.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].Loaded || list[0].ID != "docker" {
		t.Fatalf("list = %#v", list)
	}
	health, err := restored.Health(context.Background(), "docker")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("health = %+v", health)
	}
}

func helperPluginDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	link := filepath.Join(dir, "bin", "plugin-helper")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	spec := pluginhost.PluginYAMLFromManifest(contract.Manifest{
		APIVersion:    contract.ManifestAPIVersion,
		Kind:          "Connector",
		ID:            "docker",
		Version:       "test",
		ResourceTypes: []string{"container"},
		Operations: []contract.ManifestOperation{
			{Name: "logs", Effect: contract.EffectReadSensitive, InputSchema: contract.ObjectSchema(map[string]any{"container": contract.StringSchema("Container.")})},
		},
	}, pluginhost.Entrypoint{
		Command: "bin/plugin-helper",
		Args:    []string{"-test.run=TestPluginConnectorSDKHelperProcess"},
	})

	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginhost.PluginYAMLFilename), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir
}

func mustManagedPluginService(t *testing.T, statePath string) *ManagedPluginConnectorService {
	t.Helper()

	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", nil, statePath)
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	return svc
}

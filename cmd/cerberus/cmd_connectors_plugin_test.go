package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	sdksubprocess "github.com/hollis-labs/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

type commandTestPlugin struct{}

func (commandTestPlugin) Init(context.Context, sdksubprocess.InitParams) (sdksubprocess.InitResult, error) {
	return sdksubprocess.InitResult{
		ID:          "docker",
		Name:        "Command Test Plugin",
		Version:     "test",
		Description: "command helper",
		Protocol:    sdksubprocess.ProtocolVersion,
	}, nil
}

func (commandTestPlugin) Load(context.Context) (sdksubprocess.LoadResult, error) {
	return sdksubprocess.LoadResult{}, nil
}

func (commandTestPlugin) Unload(context.Context) error { return nil }

func (commandTestPlugin) Health(context.Context) (sdksubprocess.HealthStatus, error) {
	return sdksubprocess.HealthStatus{OK: true, Message: "ready"}, nil
}

func (commandTestPlugin) MCPCallTool(_ context.Context, req sdksubprocess.MCPCallRequest) (sdksubprocess.MCPCallResult, error) {
	if req.ToolName == "cerberus_docker_fail" {
		return cerbplugin.ErrorResult(cerbplugin.ErrorUnavailable, "upstream is down"), nil
	}
	data, err := json.Marshal(map[string]any{
		"tool": req.ToolName,
		"args": req.Arguments,
	})
	if err != nil {
		return sdksubprocess.MCPCallResult{}, err
	}
	return sdksubprocess.MCPCallResult{Content: data}, nil
}

func TestConnectorsPluginSDKHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CONNECTORS_PLUGIN_HELPER") != "1" {
		return
	}
	if err := sdksubprocess.Serve(commandTestPlugin{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRunPluginHealth(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	var out bytes.Buffer

	err := runPluginHealth(context.Background(), &out, pluginDir, false)
	if err != nil {
		t.Fatalf("runPluginHealth: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["id"] != "docker" || payload["healthy"] != true {
		t.Fatalf("health payload = %#v", payload)
	}
}

func TestRunPluginExec(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	var out bytes.Buffer

	err := runPluginExec(context.Background(), &out, pluginDir, "logs", map[string]any{
		"container": "web",
		"lines":     "25",
	}, false, false, false)
	if err != nil {
		t.Fatalf("runPluginExec: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["connector"] != "docker" || payload["operation"] != "logs" {
		t.Fatalf("exec payload = %#v", payload)
	}
	data := payload["data"].(map[string]any)
	if data["tool"] != "cerberus_docker_logs" {
		t.Fatalf("tool payload = %#v", data)
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
			{Name: "logs", Effect: contract.EffectReadSensitive, InputSchema: contract.ObjectSchema(map[string]any{
				"container": contract.StringSchema("Container name."),
				"lines":     contract.IntegerSchema("Lines to return."),
			})},
			// tail has no typed payload in the admin lane, which decodes a
			// built-in id's known operations; logs would be decoded as the
			// docker built-in's string. A real plugin cannot claim a
			// built-in id, so only this fixture meets that.
			{Name: "fail", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
			{Name: "tail", Effect: contract.EffectReadSensitive, InputSchema: contract.ObjectSchema(map[string]any{
				"container": contract.StringSchema("Container name."),
				"lines":     contract.IntegerSchema("Lines to return."),
			})},
		},
	}, pluginhost.Entrypoint{
		Command: "bin/plugin-helper",
		Args:    []string{"-test.run=TestConnectorsPluginSDKHelperProcess"},
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

func TestManagedPluginCommands(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	startManagedPluginSocketServer(t)

	connectorsExecFlags = connectorExecFlags{}
	connectorsPluginDev = false

	registerPendingPlugin(t, pluginDir, "docker")

	var out bytes.Buffer
	connectorsPluginManagedLoadCmd.SetOut(&out)
	connectorsPluginManagedLoadCmd.SetContext(context.Background())
	if err := connectorsPluginManagedLoadCmd.RunE(connectorsPluginManagedLoadCmd, []string{"docker"}); err != nil {
		t.Fatalf("load RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"loaded": true`) {
		t.Fatalf("load output = %s", out.String())
	}

	out.Reset()
	connectorsPluginManagedHealthCmd.SetOut(&out)
	connectorsPluginManagedHealthCmd.SetContext(context.Background())
	if err := connectorsPluginManagedHealthCmd.RunE(connectorsPluginManagedHealthCmd, []string{"docker"}); err != nil {
		t.Fatalf("health RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"healthy": true`) {
		t.Fatalf("health output = %s", out.String())
	}

	// A managed plugin's operation runs through `connectors exec`, the one
	// connector verb (`managed exec` is retired), over the daemon socket.
	out.Reset()
	connectorsExecFlags = connectorExecFlags{args: []string{"container=web", "lines=25"}}
	connectorsExecCmd.SetOut(&out)
	connectorsExecCmd.SetErr(io.Discard)
	connectorsExecCmd.SetContext(context.Background())
	if err := connectorsExecCmd.RunE(connectorsExecCmd, []string{"docker", "tail"}); err != nil {
		t.Fatalf("exec RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"connector": "docker"`) {
		t.Fatalf("exec output = %s", out.String())
	}
	// Typed by the manifest's schema on the way in, and routed through the
	// admin lane, which reaches the plugin with the integer intact.
	if !strings.Contains(out.String(), `"lines": 25`) {
		t.Fatalf("exec output = %s, want lines typed as an integer", out.String())
	}

	// An argument the schema does not declare is refused, by the daemon's
	// admin lane, which records the attempt; the CLI only hints.
	connectorsExecFlags = connectorExecFlags{args: []string{"container=web", "follow=true"}}
	err := connectorsExecCmd.RunE(connectorsExecCmd, []string{"docker", "tail"})
	if err == nil || !strings.Contains(err.Error(), "refusing fields (follow)") {
		t.Fatalf("exec with an undeclared argument: err = %v, want it refused", err)
	}
	connectorsExecFlags = connectorExecFlags{}

	// managed exec is retired, with no tombstone.
	if cmd, _, err := rootCmd.Find([]string{"connectors", "plugin", "managed", "exec"}); err == nil && cmd.Name() == "exec" {
		t.Fatal("`connectors plugin managed exec` still exists; connectors exec replaces it")
	}

	out.Reset()
	connectorsPluginManagedUnloadCmd.SetOut(&out)
	connectorsPluginManagedUnloadCmd.SetContext(context.Background())
	if err := connectorsPluginManagedUnloadCmd.RunE(connectorsPluginManagedUnloadCmd, []string{"docker"}); err != nil {
		t.Fatalf("unload RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"loaded": false`) {
		t.Fatalf("unload output = %s", out.String())
	}
}

func startManagedPluginSocketServer(t *testing.T) {
	t.Helper()

	home, err := os.MkdirTemp("/tmp", "cerbhome-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	socketPath, err := cerbapi.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	statePath, err := cerbapi.PluginConnectorStatePath()
	if err != nil {
		t.Fatalf("PluginConnectorStatePath: %v", err)
	}
	managed, err := cerbapi.NewManagedPluginConnectorService(audit.NewMemory(), "test", nil, statePath)
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	external := cerbapi.NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry(), managed)
	client := cerbapi.NewInProcessClient(
		cerbapi.WithExternalConnectorService(external),
		cerbapi.WithManagedPluginConnectorService(managed),
	)
	server := cerbapi.NewSocketServer(client, socketPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Run(ctx); err != nil && err != context.Canceled {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The retired signing flags are gone, and an old invocation fails loudly with
// cobra's unknown-flag error rather than being silently accepted.
func TestRetiredPluginSigningFlagsAreRejected(t *testing.T) {
	for _, flag := range []string{"--catalog-signed", "--archive-signed", "--archive-sha256=abc"} {
		for _, args := range [][]string{
			{"connectors", "plugin", "health", "/tmp/p", flag},
			{"connectors", "plugin", "exec", "/tmp/p", "op", flag},
			{"connectors", "plugin", "managed", "install", "/tmp/p", flag},
		} {
			rootCmd.SetArgs(args)
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(io.Discard)
			err := rootCmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("%v: err = %v, want unknown flag", args, err)
			}
		}
	}
	rootCmd.SetArgs(nil)
}

// The one-shot `plugin exec <dir>` reports a plugin's own error code the way
// the admin lane does, instead of printing the message without it.
func TestRunPluginExecReportsThePluginErrorCode(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	err := runPluginExec(context.Background(), io.Discard, helperPluginDir(t), "fail", map[string]any{}, false, false, false)
	var coded *cerbapi.ExternalConnectorError
	if !errors.As(err, &coded) || coded.Code != cerbapi.ExternalConnectorUnavailable {
		t.Fatalf("err = %v, want connector_unavailable", err)
	}
	if !strings.Contains(err.Error(), "docker fail: connector_unavailable: upstream is down") {
		t.Fatalf("the code or message was lost: %v", err)
	}
}

// registerPendingPlugin puts pluginDir in the test daemon's state as an
// entry from before install review, and has the daemon reload it by id.
// Install itself is an interactive review, tested in cmd_plugin_review_test.go.
func registerPendingPlugin(t *testing.T, pluginDir, id string) {
	t.Helper()
	statePath, err := cerbapi.PluginConnectorStatePath()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(map[string]any{"entries": []map[string]any{{"plugin_dir": pluginDir, "options": map[string]any{}, "loaded": false}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, entry, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reloadManagedPlugin(context.Background(), id); err != nil {
		t.Fatalf("reload %s: %v", id, err)
	}
}

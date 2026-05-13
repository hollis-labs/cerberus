package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/connector"
	"github.com/chrispian/cerberus/internal/pluginhost"
	contract "github.com/chrispian/cerberus/pkg/connector"
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

	err := runPluginHealth(context.Background(), &out, pluginDir, pluginTrustOptions{
		catalogSigned: true,
		archiveSigned: true,
		archiveSHA256: "abc",
	})
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
	}, false, pluginTrustOptions{
		catalogSigned: true,
		archiveSigned: true,
		archiveSHA256: "abc",
	})
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

func TestParsePluginArgs(t *testing.T) {
	cfg, err := parsePluginArgs([]string{"container=web", "lines=25"})
	if err != nil {
		t.Fatalf("parsePluginArgs: %v", err)
	}
	if cfg["container"] != "web" || cfg["lines"] != "25" {
		t.Fatalf("cfg = %#v", cfg)
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
			{Name: "logs", InputSchema: contract.ObjectSchema(map[string]any{})},
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

func TestParsePluginArgsRejectsInvalidItem(t *testing.T) {
	_, err := parsePluginArgs([]string{"broken"})
	if err == nil || !strings.Contains(err.Error(), "key=value") {
		t.Fatalf("parsePluginArgs error = %v, want key=value", err)
	}
}

func TestManagedPluginCommands(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	startManagedPluginSocketServer(t)

	connectorsPluginArgs = nil
	connectorsPluginDry = false
	connectorsPluginTrust = pluginTrustOptions{
		catalogSigned: true,
		archiveSigned: true,
		archiveSHA256: "abc",
	}

	var out bytes.Buffer
	connectorsPluginManagedInstallCmd.SetOut(&out)
	connectorsPluginManagedInstallCmd.SetContext(context.Background())
	if err := connectorsPluginManagedInstallCmd.RunE(connectorsPluginManagedInstallCmd, []string{pluginDir}); err != nil {
		t.Fatalf("install RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"id": "docker"`) {
		t.Fatalf("install output = %s", out.String())
	}

	out.Reset()
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

	out.Reset()
	connectorsPluginArgs = []string{"container=web"}
	connectorsPluginManagedExecCmd.SetOut(&out)
	connectorsPluginManagedExecCmd.SetContext(context.Background())
	if err := connectorsPluginManagedExecCmd.RunE(connectorsPluginManagedExecCmd, []string{"docker", "logs"}); err != nil {
		t.Fatalf("exec RunE: %v", err)
	}
	if !strings.Contains(out.String(), `"connector": "docker"`) {
		t.Fatalf("exec output = %s", out.String())
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
	managed, err := cerbapi.NewManagedPluginConnectorService("test", nil, statePath)
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	external := cerbapi.NewExternalConnectorService(connector.NewRegistry(), managed)
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

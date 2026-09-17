package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	sdksubprocess "github.com/hollis-labs/plugin-sdk/subprocess"
)

type sdkTestPlugin struct{}

func (sdkTestPlugin) Init(context.Context, sdksubprocess.InitParams) (sdksubprocess.InitResult, error) {
	return sdksubprocess.InitResult{
		ID:          "docker",
		Name:        "Docker Test Plugin",
		Version:     "dev",
		Description: "test helper",
		Protocol:    sdksubprocess.ProtocolVersion,
	}, nil
}

func (sdkTestPlugin) Load(context.Context) (sdksubprocess.LoadResult, error) {
	return sdksubprocess.LoadResult{}, nil
}

func (sdkTestPlugin) Unload(context.Context) error { return nil }

func (sdkTestPlugin) Health(context.Context) (sdksubprocess.HealthStatus, error) {
	return sdksubprocess.HealthStatus{OK: true, Message: "ready"}, nil
}

func (sdkTestPlugin) MCPCallTool(_ context.Context, req sdksubprocess.MCPCallRequest) (sdksubprocess.MCPCallResult, error) {
	if strings.Contains(req.ToolName, "fail") {
		return sdksubprocess.MCPCallResult{}, fmt.Errorf("tool failure")
	}
	content, err := json.Marshal(map[string]any{
		"tool": req.ToolName,
		"args": req.Arguments,
	})
	if err != nil {
		return sdksubprocess.MCPCallResult{}, err
	}
	return sdksubprocess.MCPCallResult{Content: content}, nil
}

func TestPluginSDKHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PLUGINHOST_HELPER") != "1" {
		return
	}
	if err := sdksubprocess.Serve(sdkTestPlugin{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestStdioTransportFactoryRoundTripWithPluginSDKServer(t *testing.T) {
	process, err := startSDKHelperProcess(t)
	if err != nil {
		t.Fatalf("startSDKHelperProcess: %v", err)
	}
	defer func() { _ = process.Close() }()

	initResult, err := process.Init(context.Background(), SDKInitParams{
		PluginDir: t.TempDir(),
		Config:    map[string]string{"token": "test"},
		LogLevel:  "info",
		HostInfo:  SDKHostInfo{Version: "test", Protocol: SDKProtocolVersion},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if initResult.ID != "docker" || initResult.Protocol != SDKProtocolVersion {
		t.Fatalf("InitResult = %+v", initResult)
	}
	if _, err := process.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	health, err := process.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.OK || health.Message != "ready" {
		t.Fatalf("Health = %+v", health)
	}

	result, err := process.CallTool(context.Background(), SDKMCPCallRequest{
		ToolName:  "cerberus_docker_logs",
		Arguments: map[string]any{"container": "web"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(result.Content, &data); err != nil {
		t.Fatalf("Unmarshal tool result: %v", err)
	}
	if data["tool"] != "cerberus_docker_logs" {
		t.Fatalf("tool result = %#v", data)
	}
	if err := process.Unload(context.Background()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

func TestStdioTransportFactoryPropagatesPluginError(t *testing.T) {
	process, err := startSDKHelperProcess(t)
	if err != nil {
		t.Fatalf("startSDKHelperProcess: %v", err)
	}
	defer func() { _ = process.Close() }()

	if _, err := process.Init(context.Background(), SDKInitParams{
		PluginDir: t.TempDir(),
		Config:    map[string]string{},
		LogLevel:  "info",
		HostInfo:  SDKHostInfo{Version: "test", Protocol: SDKProtocolVersion},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := process.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = process.CallTool(context.Background(), SDKMCPCallRequest{
		ToolName:  "cerberus_docker_fail",
		Arguments: map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "tool failure") {
		t.Fatalf("CallTool error = %v, want tool failure", err)
	}
}

func TestManagerLoadAndExecuteWithPluginSDKTransport(t *testing.T) {
	pluginDir := t.TempDir()
	if err := linkSelfExecutable(t, filepath.Join(pluginDir, "bin", "plugin-helper")); err != nil {
		t.Fatalf("linkSelfExecutable: %v", err)
	}

	spec := testPluginSpec(Entrypoint{
		Command: "bin/plugin-helper",
		Args:    []string{"-test.run=TestPluginSDKHelperProcess"},
	})
	spec.Cerberus.Connector.Operations = []contract.ManifestOperation{
		{Name: "logs", InputSchema: contract.ObjectSchema(map[string]any{})},
	}
	writePluginYAMLFile(t, pluginDir, spec)

	manager := NewManager(
		DirectoryInstaller{
			Policy:        DefaultTrustPolicy(),
			RequestedTier: TrustTierSigned,
			CatalogSigned: true,
			ArchiveSHA256: "abc",
			ArchiveSigned: true,
		},
		SubprocessLauncher{
			Transport: StdioTransportFactory{},
			Env: append(os.Environ(),
				"GO_WANT_PLUGINHOST_HELPER=1",
			),
		},
		DefaultTrustPolicy(),
		"test",
	)

	installed, err := manager.Install(context.Background(), pluginDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Trust.Tier != TrustTierSigned {
		t.Fatalf("Trust tier = %q, want %q", installed.Trust.Tier, TrustTierSigned)
	}
	if err := manager.Load(context.Background(), installed.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = manager.Unload(context.Background(), installed.ID) }()

	health, err := manager.Health(context.Background(), installed.ID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Loaded || !health.Healthy {
		t.Fatalf("Health = %+v", health)
	}

	result, err := manager.ExecuteOperation(context.Background(), OperationArgs{
		Connector: "docker",
		Operation: "logs",
		Config:    map[string]any{"container": "web"},
	})
	if err != nil {
		t.Fatalf("ExecuteOperation: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %#v", result.Data)
	}
	if data["tool"] != "cerberus_docker_logs" {
		t.Fatalf("tool result = %#v", data)
	}
}

func startSDKHelperProcess(t *testing.T) (*RPCProcess, error) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := helperCommand(exe)
	process, err := StdioTransportFactory{}.Start(context.Background(), cmd)
	if err != nil {
		return nil, err
	}
	rpcProcess, ok := process.(*RPCProcess)
	if !ok {
		_ = process.Close()
		return nil, fmt.Errorf("process type = %T, want *RPCProcess", process)
	}
	return rpcProcess, nil
}

func helperCommand(exe string) *exec.Cmd {
	cmd := exec.Command(exe, "-test.run=TestPluginSDKHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_PLUGINHOST_HELPER=1")
	return cmd
}

func linkSelfExecutable(t *testing.T, target string) error {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Link(exe, target); err == nil {
		return nil
	}
	return os.Symlink(exe, target)
}

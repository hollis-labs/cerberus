package pluginhost

import (
	"context"
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type fakeInstaller struct {
	plugin InstalledPlugin
	err    error
}

func (f fakeInstaller) Install(context.Context, string) (InstalledPlugin, error) {
	return f.plugin, f.err
}

type fakeProcess struct {
	initResult SDKInitResult
	loadResult SDKLoadResult
	health     SDKHealthResult
	callResult SDKMCPCallResult

	unloaded bool
	closed   bool
	calls    int
}

func (f *fakeProcess) Init(context.Context, SDKInitParams) (SDKInitResult, error) {
	return f.initResult, nil
}
func (f *fakeProcess) Load(context.Context) (SDKLoadResult, error) { return f.loadResult, nil }
func (f *fakeProcess) Unload(context.Context) error {
	f.unloaded = true
	return nil
}
func (f *fakeProcess) Health(context.Context) (SDKHealthResult, error) { return f.health, nil }
func (f *fakeProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	f.calls++
	return f.callResult, nil
}
func (f *fakeProcess) Close() error {
	f.closed = true
	return nil
}

type fakeLauncher struct {
	process Process
	err     error
}

func (f fakeLauncher) Launch(context.Context, InstalledPlugin) (Process, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.process, nil
}

func validInstalledPlugin() InstalledPlugin {
	return InstalledPlugin{
		ID:      "docker",
		Version: "dev",
		Path:    "/tmp/docker-plugin",
		Trust:   TrustDecision{Tier: TrustTierSigned},
		Spec:    testPluginSpec(Entrypoint{Command: "bin/docker-plugin"}),
		Manifest: contract.Manifest{
			APIVersion:    contract.ManifestAPIVersion,
			Kind:          "Connector",
			ID:            "docker",
			Version:       "dev",
			ResourceTypes: []string{"container"},
			Operations: []contract.ManifestOperation{
				{Name: "logs", InputSchema: contract.ObjectSchema(map[string]any{})},
			},
		},
	}
}

func TestManagerInstallAndLoad(t *testing.T) {
	plugin := validInstalledPlugin()
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
		health:     SDKHealthResult{OK: true, Message: "ready"},
	}
	manager := NewManager(fakeInstaller{plugin: plugin}, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")

	installed, err := manager.Install(context.Background(), "local")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.ID != plugin.ID {
		t.Fatalf("Install ID = %q, want %q", installed.ID, plugin.ID)
	}
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}

	health, err := manager.Health(context.Background(), plugin.ID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Loaded || !health.Healthy {
		t.Fatalf("Health = %+v", health)
	}
}

func TestManagerExecuteOperation(t *testing.T) {
	plugin := validInstalledPlugin()
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
		callResult: SDKMCPCallResult{Content: []byte(`{"ok":true}`)},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
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
	if !ok || data["ok"] != true {
		t.Fatalf("Data = %#v", result.Data)
	}
}

func TestManagerUnloadClosesProcess(t *testing.T) {
	plugin := validInstalledPlugin()
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := manager.Unload(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if !process.unloaded || !process.closed {
		t.Fatalf("process state = unloaded:%v closed:%v", process.unloaded, process.closed)
	}
}

func TestManagerLoadRejectsProtocolMismatch(t *testing.T) {
	plugin := validInstalledPlugin()
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: 99},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")
	manager.RegisterInstalled(plugin)

	err := manager.Load(context.Background(), plugin.ID)
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("Load error = %v, want protocol mismatch", err)
	}
}

func TestManagerExecuteOperationRejectsUnsignedDevDestructive(t *testing.T) {
	plugin := validInstalledPlugin()
	plugin.Trust = TrustDecision{Tier: TrustTierUnsignedDev}
	plugin.Manifest.Operations = []contract.ManifestOperation{
		{Name: "destroy", Destructive: true, RequiresAck: true, InputSchema: contract.ObjectSchema(map[string]any{})},
	}
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{
		Connector: "docker",
		Operation: "destroy",
	})
	if err == nil || !strings.Contains(err.Error(), "not agent-auto executable") {
		t.Fatalf("ExecuteOperation error = %v, want trust-tier rejection", err)
	}
}

func TestManagerExecuteOperationRequiresAcknowledgmentForSignedDestructive(t *testing.T) {
	plugin := validInstalledPlugin()
	plugin.Manifest.Operations = []contract.ManifestOperation{
		{Name: "destroy", Destructive: true, RequiresAck: true, InputSchema: contract.ObjectSchema(map[string]any{})},
	}
	process := &fakeProcess{
		initResult: SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion},
	}
	manager := NewManager(nil, fakeLauncher{process: process}, DefaultTrustPolicy(), "test")
	manager.RegisterInstalled(plugin)
	if err := manager.Load(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{
		Connector: "docker",
		Operation: "destroy",
	})
	if err == nil || !strings.Contains(err.Error(), "requires operator acknowledgment") {
		t.Fatalf("ExecuteOperation error = %v, want acknowledgment rejection", err)
	}
}

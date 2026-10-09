package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

func newTestManager(t *testing.T, installer Installer, launcher Launcher, version string, opts ...ManagerOption) *Manager {
	t.Helper()
	root := t.TempDir()
	opts = append([]ManagerOption{WithRuntimeRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))}, opts...)
	return NewManager(installer, launcher, version, opts...)
}

// A real SDK subprocess receives the complete handshake, and a fresh owner
// generation on every reload; the bundle and reverse authority remain separate.
func TestManagerProtocol2RuntimeAcrossReload(t *testing.T) {
	bundle := t.TempDir()
	if err := linkSelfExecutable(t, filepath.Join(bundle, "bin", "plugin-helper")); err != nil {
		t.Fatal(err)
	}
	spec := testPluginSpec(Entrypoint{Command: "bin/plugin-helper", Args: []string{"-test.run=TestPluginSDKHelperProcess"}})
	spec.Cerberus.Connector.Operations = []contract.ManifestOperation{{Name: "logs", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})}}
	writePluginYAMLFile(t, bundle, spec)
	manager := newTestManager(t, DirectoryInstaller{Policy: LocalInstallPolicy()}, SubprocessLauncher{Transport: StdioTransportFactory{}, Env: append(os.Environ(), "GO_WANT_PLUGINHOST_HELPER=1")}, "test")
	installed, err := manager.Install(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	var previous capability.RuntimeIdentity
	var dataDir, cacheDir string
	for range 2 {
		if err := manager.Load(context.Background(), installed.ID); err != nil {
			t.Fatal(err)
		}
		result, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: installed.ID, Operation: "logs", Config: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(result.Data)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Init SDKInitParams `json:"init"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		got := response.Init
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
		if got.Incarnation.OwnerID != installed.ID || got.HostInfo.Protocol != SDKProtocolVersion || got.CapabilityContract != capability.ContractVersion {
			t.Fatalf("wrong Init agreement: %+v", got)
		}
		if len(got.Grants) != 0 || got.HostServices != nil || got.HooksProfile != nil {
			t.Fatal("forward-only host offered reverse authority or profiles")
		}
		if got.DataDir == bundle || got.CacheDir == bundle || got.DataDir == got.CacheDir {
			t.Fatal("bundle and writable roots overlap")
		}
		if previous.OwnerGeneration != 0 {
			if got.Incarnation.HostInstance != previous.HostInstance || got.Incarnation.OwnerGeneration <= previous.OwnerGeneration {
				t.Fatal("reload reused runtime generation")
			}
			if got.DataDir != dataDir || got.CacheDir != cacheDir {
				t.Fatal("reload changed installation roots")
			}
		}
		previous, dataDir, cacheDir = got.Incarnation, got.DataDir, got.CacheDir
		if err := manager.Unload(context.Background(), installed.ID); err != nil {
			t.Fatal(err)
		}
	}
}

type runtimeLauncher struct {
	calls   int
	process Process
}

func (l *runtimeLauncher) Launch(context.Context, InstalledPlugin) (Process, error) {
	l.calls++
	return l.process, nil
}

type runtimeProcess struct {
	fakeProcess
	loaded bool
}

func (p *runtimeProcess) Load(context.Context) (SDKLoadResult, error) {
	p.loaded = true
	return SDKLoadResult{}, nil
}

func TestManagerRuntimeRefusalBeforeSpawn(t *testing.T) {
	plugin := validInstalledPlugin()
	plugin.Path = t.TempDir()
	link := filepath.Join(t.TempDir(), "bundle-link")
	if err := os.Symlink(plugin.Path, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]ManagerOption{
		"symlink into bundle": WithRuntimeRoots(filepath.Join(link, "data"), filepath.Join(t.TempDir(), "cache")),
		"missing":             WithRuntimeRoots("", ""),
		"relative":            WithRuntimeRoots("data", "cache"),
		"bundle":              WithRuntimeRoots(filepath.Join(plugin.Path, "data"), filepath.Join(t.TempDir(), "cache")),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			launcher := &runtimeLauncher{}
			manager := NewManager(nil, launcher, "test", opt)
			manager.RegisterInstalled(plugin)
			if err := manager.Load(context.Background(), plugin.ID); err == nil {
				t.Fatal("invalid roots accepted")
			}
			if launcher.calls != 0 {
				t.Fatal("spawned before validating roots")
			}
		})
	}
}

func TestManagerRefusesInitBeforeLoad(t *testing.T) {
	plugin := validInstalledPlugin()
	reverse := 1
	for _, tc := range []struct {
		name   string
		result SDKInitResult
	}{
		{"old protocol", SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: 1, CapabilityContract: 1}},
		{"missing contract", SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion}},
		{"wrong identity", SDKInitResult{ID: "other", Version: plugin.Version, Protocol: SDKProtocolVersion, CapabilityContract: 1}},
		{"wrong version", SDKInitResult{ID: plugin.ID, Version: "other", Protocol: SDKProtocolVersion, CapabilityContract: 1}},
		{"unsolicited reverse", SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion, CapabilityContract: 1, ReverseRPCVersion: &reverse}},
		{"unsolicited hooks", SDKInitResult{ID: plugin.ID, Version: plugin.Version, Protocol: SDKProtocolVersion, CapabilityContract: 1, HooksProfileVersion: &reverse}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := &runtimeProcess{fakeProcess: fakeProcess{initResult: tc.result}}
			launcher := &runtimeLauncher{process: process}
			manager := newTestManager(t, nil, launcher, "test")
			manager.RegisterInstalled(plugin)
			if err := manager.Load(context.Background(), plugin.ID); err == nil {
				t.Fatal("invalid agreement accepted")
			}
			if process.loaded {
				t.Fatal("Load ran after refused Init")
			}
		})
	}
}

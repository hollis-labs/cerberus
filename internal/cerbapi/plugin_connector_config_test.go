package cerbapi

import (
	"context"
	"encoding/json"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func configuredManagedService(t *testing.T, body string) (*ManagedPluginConnectorService, pluginhost.InstalledPlugin) {
	t.Helper()
	path := filepath.Join(t.TempDir(), pluginhost.ConnectorConfigFilename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewManagedPluginConnectorService: %v", err)
	}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: &echoingPluginProcess{}}, "test",
		pluginhost.WithConnectorConfig(connectorConfigLoader(path)))
	plugin := pluginhost.InstalledPlugin{
		ID:     "leaky",
		Origin: pluginhost.OriginInstalled,
		Manifest: contract.Manifest{
			APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "leaky", Version: "dev",
			Config: contract.ConfigSchema{Fields: []contract.ConfigField{{Name: "address", Type: "string"}}},
			Operations: []contract.ManifestOperation{
				{Name: "list_things", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
			},
		},
	}
	svc.manager.RegisterInstalled(plugin)
	return svc, plugin
}

// managed list reports what the file gave a plugin — names and a
// fingerprint, never values — and, for a plugin it refused, why.
func TestManagedListReportsConnectorConfig(t *testing.T) {
	svc, plugin := configuredManagedService(t, "leaky:\n  fields: {address: http://secret-host.test}\n  mcp: {expose: [list_things]}\n")
	if err := svc.manager.Load(context.Background(), "leaky"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	state := svc.state(plugin, true)
	if strings.Join(state.ConfigFields, ",") != "address" || strings.Join(state.MCPExpose, ",") != "list_things" || len(state.ConfigSHA256) != 64 {
		t.Fatalf("state = %+v", state)
	}
	data, _ := redact.Marshal(state)
	if strings.Contains(string(data), "secret-host.test") {
		t.Fatalf("managed list carries a field value: %s", data)
	}
	for _, key := range []string{`"config_fields":["address"]`, `"mcp_expose":["list_things"]`, `"config_problems":[]`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("state JSON lacks %s: %s", key, data)
		}
	}
}

func TestManagedListExplainsARefusedConfig(t *testing.T) {
	svc, plugin := configuredManagedService(t, "leaky:\n  mcp: {expose: [drop_tables]}\n")
	if err := svc.manager.Load(context.Background(), "leaky"); err == nil {
		t.Fatal("Load succeeded with an undeclared operation in mcp.expose")
	}
	state := svc.state(plugin, false)
	if state.Loaded || len(state.ConfigProblems) != 1 || !strings.Contains(state.ConfigProblems[0], `"drop_tables"`) {
		t.Fatalf("state = %+v", state)
	}
	data, _ := json.Marshal(state)
	for _, key := range []string{`"config_fields":[]`, `"mcp_expose":[]`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("state JSON lacks %s: %s", key, data)
		}
	}
}

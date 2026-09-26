package cerbapi

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// exposureService is a loaded plugin with three operations, one CLI-only,
// under the given connector-config.yaml.
func exposureService(t *testing.T, config string) (*ManagedPluginConnectorService, pluginhost.InstalledPlugin) {
	t.Helper()
	path := filepath.Join(t.TempDir(), pluginhost.ConnectorConfigFilename)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.manager = pluginhost.NewManager(nil, echoingLauncher{process: &echoingPluginProcess{}}, "test",
		pluginhost.WithConnectorConfig(connectorConfigLoader(path)))
	op := func(name string) contract.ManifestOperation {
		return contract.ManifestOperation{Name: name, Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})}
	}
	plugin := pluginhost.InstalledPlugin{ID: "leaky", Origin: pluginhost.OriginInstalled,
		Spec: pluginsdk.PluginYAML{Cerberus: pluginsdk.CerberusPluginBlock{Surfaces: pluginsdk.Surfaces{CLIOnly: []string{"dump_all"}}}},
		Manifest: contract.Manifest{APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "leaky", Version: "dev",
			Operations: []contract.ManifestOperation{op("list_things"), op("get_thing"), op("dump_all")}}}
	svc.manager.RegisterInstalled(plugin)
	if err := svc.manager.Load(context.Background(), "leaky"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = svc.manager.Unload(context.Background(), "leaky") })
	return svc, plugin
}

// MCP exposure: nothing unless listed, under secure; every declared
// operation but the CLI-only ones under a global permissive posture; and an
// operator's mcp.expose list — even an empty one — narrows under both.
func TestMCPExposureFollowsThePostureAndTheOperatorsList(t *testing.T) {
	global := policy.File{Posture: policy.PosturePermissive}
	scoped := policy.File{PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}}
	for _, c := range []struct {
		name, config string
		posture      policy.File
		want         string
		byPosture    bool
	}{
		{"secure, no list: nothing", "{}\n", policy.File{}, "", false},
		{"a scoped rule is not a host-wide switch", "{}\n", scoped, "", false},
		{"permissive, no list: every declared op but the CLI-only one", "{}\n", global, "get_thing,list_things", true},
		{"permissive, the operator's list narrows", "leaky:\n  mcp: {expose: [get_thing]}\n", global, "get_thing", false},
		{"permissive, an empty list is still a list", "leaky:\n  mcp: {expose: []}\n", global, "", false},
		{"secure, the operator's list", "leaky:\n  mcp: {expose: [list_things]}\n", policy.File{}, "list_things", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			withPosture(t, c.posture)
			svc, plugin := exposureService(t, c.config)
			st := svc.state(plugin, true)
			if strings.Join(st.MCPExpose, ",") != c.want || st.MCPExposeByPosture != c.byPosture {
				t.Fatalf("exposure = %v (by posture %t), want %q (%t)", st.MCPExpose, st.MCPExposeByPosture, c.want, c.byPosture)
			}
		})
	}
}

// An insecure listener is an admin event on the record, under the posture
// that allowed it.
func TestRecordInsecureListen(t *testing.T) {
	withPosture(t, policy.File{Posture: policy.PosturePermissive})
	sink := audit.NewMemory()
	if err := RecordInsecureListen(BeginRequest(context.Background(), SurfaceInProcess), sink, "mcp-http", "0.0.0.0:4785", []string{"box.lan"}); err != nil {
		t.Fatal(err)
	}
	recs := sink.Records()
	if len(recs) != 2 || recs[0].Connector != "mcp-http" || recs[0].Operation != "insecure_listen" || recs[0].Effect != "admin" ||
		recs[0].Target.Fields["listen"] != "0.0.0.0:4785" || recs[1].OutcomeCode != audit.OutcomeOK || recs[0].Posture != policy.PosturePermissive {
		t.Fatalf("records %+v", recs)
	}
	if err := RecordInsecureListen(BeginRequest(context.Background(), SurfaceInProcess), audit.Failing{}, "mcp-http", "0.0.0.0:4785", nil); err == nil {
		t.Fatal("an unwritable log must refuse the listener")
	}
}

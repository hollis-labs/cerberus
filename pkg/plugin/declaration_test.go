package plugin

import (
	"strings"
	"testing"
)

func TestDeclarationsAreCheckedAgainstTheConnector(t *testing.T) {
	spec := PluginYAMLFromManifest(validManifest(), Entrypoint{Command: "bin/plugin"})
	op := spec.Cerberus.Connector.Operations[0].Name
	spec.Cerberus.SuggestedPolicy = []SuggestedRule{{Operation: op, Require: RequireApproval}}
	spec.Cerberus.Surfaces = Surfaces{MCP: []string{op}}
	spec.Cerberus.Telemetry = []TelemetryDeclaration{{Operation: op, Events: []string{"step"}}}
	spec.Cerberus.Host = HostRange{MinContract: 1, MaxContract: 1}
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatalf("valid declarations refused: %v", err)
	}
	for name, mutate := range map[string]func(*PluginYAML){
		"unknown op":        func(p *PluginYAML) { p.Cerberus.SuggestedPolicy[0].Operation = "nope" },
		"unknown require":   func(p *PluginYAML) { p.Cerberus.SuggestedPolicy[0].Require = "maybe" },
		"mcp and cli_only":  func(p *PluginYAML) { p.Cerberus.Surfaces.CLIOnly = []string{op} },
		"empty telemetry":   func(p *PluginYAML) { p.Cerberus.Telemetry[0].Events = nil },
		"inverted range":    func(p *PluginYAML) { p.Cerberus.Host = HostRange{MinContract: 3, MaxContract: 2} },
		"negative contract": func(p *PluginYAML) { p.Cerberus.Host = HostRange{MinContract: -1} },
	} {
		bad := spec
		bad.Cerberus.SuggestedPolicy = append([]SuggestedRule(nil), spec.Cerberus.SuggestedPolicy...)
		bad.Cerberus.Telemetry = append([]TelemetryDeclaration(nil), spec.Cerberus.Telemetry...)
		mutate(&bad)
		if err := bad.Validate(t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHostRangeCheck(t *testing.T) {
	if err := (HostRange{}).Check(ContractVersion); err != nil {
		t.Fatal(err)
	}
	if err := (HostRange{MinContract: ContractVersion + 1}).Check(ContractVersion); err == nil || !strings.Contains(err.Error(), "upgrade Cerberus") {
		t.Fatalf("min: %v", err)
	}
	if err := (HostRange{MaxContract: 1}).Check(2); err == nil || !strings.Contains(err.Error(), "rebuild the plugin") {
		t.Fatalf("max: %v", err)
	}
}

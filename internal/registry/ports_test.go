package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

func TestDuplicatePortsRejectRegistrationAndExposeLiveEdits(t *testing.T) {
	first := writeFile(t, "one.cerberus.yaml", strings.ReplaceAll(validProjectConfigYAML, "clockwork", "first"))
	second := writeFile(t, "two.cerberus.yaml", strings.ReplaceAll(validProjectConfigYAML, "clockwork", "second"))
	reg := New(filepath.Join(t.TempDir(), DefaultIndexFilename))
	if _, err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(second); err == nil || !strings.Contains(err.Error(), "first-api") || !strings.Contains(err.Error(), "second-api") {
		t.Fatalf("cross-project collision accepted: %v", err)
	}
	entries, err := reg.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Owner == "second" {
			t.Fatal("failed registration partially persisted")
		}
	}
	unique := strings.ReplaceAll(strings.ReplaceAll(validProjectConfigYAML, "clockwork", "second"), "8080", "8081")
	if err = os.WriteFile(second, []byte(unique), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Register(second); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(second, []byte(strings.ReplaceAll(unique, "8081", "8080")), 0600); err != nil {
		t.Fatal(err)
	}
	reports, err := reg.Health()
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range reports {
		if report.Healthy() || !strings.Contains(report.Detail, "TCP port 8080") {
			t.Fatalf("health hid conflict: %+v", report)
		}
	}
	resolved, err := Resolve(ResolveOptions{IndexPath: reg.IndexPath()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(resolved.Warnings, " "), "TCP port 8080") {
		t.Fatal("resolve hid conflict")
	}
	if len(PortConflicts(resolved.Config.Resources)) == 0 {
		t.Fatal("conflicting resources disappeared from inspection")
	}
}

func TestDuplicatePortWithinConfigAndRemotePortIsolation(t *testing.T) {
	pc, err := LoadProjectConfig(writeFile(t, "app.cerberus.yaml", validProjectConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	pc.Resources[1].Config = map[string]any{"port": 8080}
	result := ValidateProjectConfig(pc)
	if len(result.PortConflictIssues()) == 0 {
		t.Fatal("duplicate port passed author validation")
	}
	pc.Resources[1].Connector = "ssh"
	if len(PortConflicts(pc.Resources)) != 0 {
		t.Fatal("remote port considered local conflict")
	}
	pc.Resources[1].Connector = "local"
	pc.Resources[1].Config = nil
	if len(PortConflicts(pc.Resources)) != 0 {
		t.Fatal("omitted port considered a listener")
	}
}

func TestRegistryHealthIncludesGlobalPortAssignments(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(global, []byte("version: 2\nresources:\n  - id: global-api\n    type: process\n    connector: local\n    config: {port: 8080}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := ForConfig(global)
	if err != nil {
		t.Fatal(err)
	}
	project := writeFile(t, "app.cerberus.yaml", validProjectConfigYAML)
	if _, err = reg.Register(project); err == nil || !strings.Contains(err.Error(), "global-api") {
		t.Fatalf("global collision accepted: %v", err)
	}
	// The shared checker also recognizes integer values loaded over JSON.
	resources := []config.ResourceDef{{ID: "a", Type: "process", Connector: "local", Config: map[string]any{"port": float64(8080)}}, {ID: "b", Type: "process", Connector: "local", Config: map[string]any{"port": 8080}}}
	if len(PortConflicts(resources)) == 0 {
		t.Fatal("JSON port collision missed")
	}
}

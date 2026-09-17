package plugin

import (
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestPluginYAMLFromManifestValidates(t *testing.T) {
	manifest := validManifest()
	pluginYAML := PluginYAMLFromManifest(manifest, Entrypoint{Command: "bin/docker-plugin", Args: []string{"serve"}})

	if err := pluginYAML.Validate(t.TempDir()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPluginYAMLRejectsShellEntrypoint(t *testing.T) {
	pluginYAML := PluginYAMLFromManifest(validManifest(), Entrypoint{Command: "bin/docker-plugin --serve"})

	err := pluginYAML.Validate(t.TempDir())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "shell string") {
		t.Fatalf("error = %v, want shell string", err)
	}
}

func TestPluginYAMLRejectsMismatchedConnectorID(t *testing.T) {
	manifest := validManifest()
	manifest.ID = "github"
	pluginYAML := PluginYAMLFromManifest(manifest, Entrypoint{Command: "bin/plugin"})
	pluginYAML.ID = "docker"

	err := pluginYAML.Validate(t.TempDir())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "id must match") {
		t.Fatalf("error = %v, want id match", err)
	}
}

func TestEntrypointRejectsEscapingPluginDir(t *testing.T) {
	err := (Entrypoint{Command: "../plugin"}).Validate(t.TempDir())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "inside the plugin directory") {
		t.Fatalf("error = %v, want inside plugin directory", err)
	}
}

func TestPluginYAMLRejectsInvalidConnectorManifest(t *testing.T) {
	pluginYAML := PluginYAMLFromManifest(contract.Manifest{}, Entrypoint{Command: "bin/plugin"})

	err := pluginYAML.Validate(t.TempDir())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "connector manifest") {
		t.Fatalf("error = %v, want connector manifest", err)
	}
}

func validManifest() contract.Manifest {
	return contract.Manifest{
		APIVersion:    contract.ManifestAPIVersion,
		Kind:          "Connector",
		ID:            "docker",
		Version:       "dev",
		ResourceTypes: []string{"container"},
		Operations: []contract.ManifestOperation{
			{Name: "status", InputSchema: contract.ObjectSchema(map[string]any{})},
		},
	}
}

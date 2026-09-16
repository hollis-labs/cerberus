package dockerplugin

import (
	"fmt"
	"os"
	"path/filepath"

	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	contract "github.com/chrispian/cerberus/pkg/connector"
	plugin "github.com/chrispian/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

const BinaryName = "cerberus-docker-plugin"

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(dockerconn.Definition())
}

func PluginYAML() plugin.PluginYAML {
	return plugin.PluginYAMLFromManifest(Manifest(), plugin.Entrypoint{
		Command: filepath.ToSlash(filepath.Join("bin", BinaryName)),
	})
}

func WritePrototype(dir string) error {
	if dir == "" {
		return fmt.Errorf("prototype directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return fmt.Errorf("create prototype directories: %w", err)
	}

	spec := PluginYAML()
	data, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal plugin.yaml: %w", err)
	}
	path := filepath.Join(dir, plugin.PluginYAMLFilename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write plugin.yaml: %w", err)
	}
	return nil
}

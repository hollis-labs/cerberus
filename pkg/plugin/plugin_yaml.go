package plugin

import (
	"fmt"
	"path/filepath"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// PluginYAMLFilename is the metadata file every plugin directory carries.
const PluginYAMLFilename = "plugin.yaml"

// PluginYAML is the Cerberus-owned plugin descriptor. It pairs the subprocess
// entrypoint with the connector manifest the host installs and routes against.
type PluginYAML struct {
	SchemaVersion string              `json:"schema_version" yaml:"schema_version"`
	ID            string              `json:"id" yaml:"id"`
	Version       string              `json:"version" yaml:"version"`
	Protocol      string              `json:"protocol" yaml:"protocol"`
	Runtime       string              `json:"runtime" yaml:"runtime"`
	Entrypoint    Entrypoint          `json:"entrypoint" yaml:"entrypoint"`
	Cerberus      CerberusPluginBlock `json:"cerberus" yaml:"cerberus"`
}

// Entrypoint is the executable the host launches, relative to the plugin
// directory.
type Entrypoint struct {
	Command string   `json:"command" yaml:"command"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// CerberusPluginBlock carries the Cerberus-specific half of plugin.yaml.
type CerberusPluginBlock struct {
	Connector contract.Manifest `json:"connector" yaml:"connector"`
}

// PluginYAMLFromManifest builds the descriptor for a subprocess plugin serving
// the given connector manifest.
func PluginYAMLFromManifest(manifest contract.Manifest, entrypoint Entrypoint) PluginYAML {
	return PluginYAML{
		SchemaVersion: "1",
		ID:            manifest.ID,
		Version:       manifest.Version,
		Protocol:      "plugin-sdk/subprocess",
		Runtime:       "subprocess",
		Entrypoint:    entrypoint,
		Cerberus: CerberusPluginBlock{
			Connector: manifest,
		},
	}
}

// Validate reports every problem with the descriptor at once.
func (p PluginYAML) Validate(pluginDir string) error {
	var problems []string
	if p.SchemaVersion == "" {
		problems = append(problems, "schema_version is required")
	}
	if p.ID == "" {
		problems = append(problems, "id is required")
	}
	if p.Version == "" {
		problems = append(problems, "version is required")
	}
	if p.Protocol != "plugin-sdk/subprocess" {
		problems = append(problems, `protocol must be "plugin-sdk/subprocess"`)
	}
	if p.Runtime != "subprocess" {
		problems = append(problems, `runtime must be "subprocess"`)
	}
	if err := p.Entrypoint.Validate(pluginDir); err != nil {
		problems = append(problems, err.Error())
	}
	if err := p.Cerberus.Connector.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if p.Cerberus.Connector.ID != "" && p.ID != "" && p.Cerberus.Connector.ID != p.ID {
		problems = append(problems, "plugin id must match cerberus connector id")
	}
	if len(problems) > 0 {
		return fmt.Errorf("plugin.yaml %q invalid: %s", p.ID, strings.Join(problems, "; "))
	}
	return nil
}

// Validate rejects entrypoints that are shell strings, absolute, or escape the
// plugin directory.
func (e Entrypoint) Validate(pluginDir string) error {
	if e.Command == "" {
		return fmt.Errorf("entrypoint command is required")
	}
	if strings.ContainsAny(e.Command, " \t\n\r;&|`$<>") {
		return fmt.Errorf("entrypoint command must be a single executable path, not a shell string")
	}
	if filepath.IsAbs(e.Command) {
		return fmt.Errorf("entrypoint command must be relative to the plugin directory")
	}
	clean := filepath.Clean(e.Command)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("entrypoint command must stay inside the plugin directory")
	}
	for _, arg := range e.Args {
		if strings.ContainsAny(arg, "\x00\n\r") {
			return fmt.Errorf("entrypoint args cannot contain control separators")
		}
	}
	return nil
}

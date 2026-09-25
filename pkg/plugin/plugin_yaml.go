package plugin

import (
	"fmt"
	"path/filepath"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// PluginYAMLFilename is the metadata file every plugin directory carries.
const PluginYAMLFilename = "plugin.yaml"

// PluginYAML is the Cerberus-owned plugin descriptor. It pairs the subprocess
// entrypoint with the connector manifest the host installs and routes against.
type PluginYAML struct {
	SchemaVersion string     `json:"schema_version" yaml:"schema_version"`
	ID            string     `json:"id" yaml:"id"`
	Version       string     `json:"version" yaml:"version"`
	Protocol      string     `json:"protocol" yaml:"protocol"`
	Runtime       string     `json:"runtime" yaml:"runtime"`
	Entrypoint    Entrypoint `json:"entrypoint" yaml:"entrypoint"`

	// Capabilities are the ambient host access this plugin asks for, in the
	// host's vocabulary. They describe the *process*, not the connector, which
	// is why they sit beside the entrypoint rather than inside the manifest.
	//
	// Declared here rather than requested in code so that what a plugin wants
	// is reviewable before it runs. A plugin that declares nothing receives
	// nothing: Cerberus's launch environment carries no credential handle by
	// default, and an undeclared capability is not granted.
	//
	// The type is the SDK's, so the shape is the same one any host built on
	// plugin-sdk embeds. The names are Cerberus's — see
	// internal/pluginhost/capability.go for the vocabulary this host honors.
	Capabilities []subprocess.CapabilityRequest `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`

	Cerberus CerberusPluginBlock `json:"cerberus" yaml:"cerberus"`
}

// Entrypoint is the executable the host launches, relative to the plugin
// directory.
type Entrypoint struct {
	Command string   `json:"command" yaml:"command"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// CerberusPluginBlock carries the Cerberus-specific half of plugin.yaml: the
// connector manifest, and the declarations the install review shows beside
// it (docs/plans/live-systems-security-target.md, section 10). Every
// declaration is the plugin's claim about itself. The host shows it, uses it
// only to narrow what the plugin can reach, and never widens anything on it.
type CerberusPluginBlock struct {
	Connector contract.Manifest `json:"connector" yaml:"connector"`

	// Host is the Cerberus contract range the plugin was built for. Enforced
	// at install and at load.
	Host HostRange `json:"host,omitzero" yaml:"host,omitempty"`
	// SuggestedPolicy is shown at install and never applied (I10).
	SuggestedPolicy []SuggestedRule `json:"suggested_policy,omitempty" yaml:"suggested_policy,omitempty"`
	// Surfaces suggests MCP exposure and marks CLI-only operations.
	Surfaces Surfaces `json:"surfaces,omitzero" yaml:"surfaces,omitempty"`
	// Telemetry names the events each operation reports.
	Telemetry []TelemetryDeclaration `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`
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
	problems = append(problems, p.Cerberus.validateDeclarations()...)
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

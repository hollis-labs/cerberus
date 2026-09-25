package connector

import (
	"fmt"
	"strings"
)

const ManifestAPIVersion = "cerberus.connector/v1"

// Manifest is the Cerberus-owned connector declaration that can be embedded
// into plugin.yaml metadata or generated for build-time connectors.
type Manifest struct {
	APIVersion    string              `json:"api_version" yaml:"api_version"`
	Kind          string              `json:"kind" yaml:"kind"`
	ID            string              `json:"id" yaml:"id"`
	Version       string              `json:"version" yaml:"version"`
	ResourceTypes []string            `json:"resource_types" yaml:"resource_types"`
	Capabilities  Capabilities        `json:"capabilities" yaml:"capabilities"`
	Config        ConfigSchema        `json:"config,omitempty" yaml:"config,omitempty"`
	Operations    []ManifestOperation `json:"operations" yaml:"operations"`
}

// ManifestOperation extends public operation metadata with host policy fields.
type ManifestOperation struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Examples    []string       `json:"examples,omitempty" yaml:"examples,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	// Destructive operations always require operator acknowledgment. The host
	// enforces that from this field alone.
	Destructive bool `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	// SupportsDry declares that the plugin honors dry_run with a preview that
	// executes nothing. A dry run of an operation without it is refused by the
	// host as preview_unsupported and never reaches the plugin. The preview is
	// the plugin's claim; the host does not verify it.
	SupportsDry bool `json:"supports_dry,omitempty" yaml:"supports_dry,omitempty"`
	// RequiresAck is deprecated: the host gates on Destructive and ignores
	// this field. Validation still requires it to be true on a destructive
	// operation, because older hosts required both, so dropping it would run a
	// destructive operation unacknowledged there.
	RequiresAck bool `json:"requires_ack,omitempty" yaml:"requires_ack,omitempty"`
}

// ManifestFromDefinition converts build-time connector metadata into the
// Cerberus manifest shape expected by future plugin adapters.
func ManifestFromDefinition(def Definition) Manifest {
	ops := make([]ManifestOperation, 0, len(def.Operations))
	for _, op := range def.Operations {
		ops = append(ops, ManifestOperation{
			Name:        op.Name,
			Description: op.Description,
			Examples:    append([]string(nil), op.Examples...),
			InputSchema: cloneSchema(op.InputSchema),
			Destructive: op.Destructive,
			SupportsDry: op.SupportsDry,
			RequiresAck: op.Destructive,
		})
	}
	return Manifest{
		APIVersion:    ManifestAPIVersion,
		Kind:          "Connector",
		ID:            def.ID,
		Version:       def.Version,
		ResourceTypes: append([]string(nil), def.ResourceTypes...),
		Capabilities:  def.Capabilities,
		Config:        def.Config,
		Operations:    ops,
	}
}

// DefinitionFromManifest converts plugin/runtime connector metadata back into
// the public connector discovery shape used by CLI, API, and MCP surfaces.
func DefinitionFromManifest(manifest Manifest) Definition {
	ops := make([]Operation, 0, len(manifest.Operations))
	for _, op := range manifest.Operations {
		ops = append(ops, Operation{
			Name:        op.Name,
			Description: op.Description,
			Examples:    append([]string(nil), op.Examples...),
			InputSchema: cloneSchema(op.InputSchema),
			Destructive: op.Destructive,
			SupportsDry: op.SupportsDry,
		})
	}
	return Definition{
		ID:            manifest.ID,
		Version:       manifest.Version,
		ResourceTypes: append([]string(nil), manifest.ResourceTypes...),
		Capabilities:  manifest.Capabilities,
		Config:        manifest.Config,
		Operations:    ops,
	}
}

// Validate checks the manifest fields that Cerberus needs before trusting a
// built-in or plugin connector declaration.
func (m Manifest) Validate() error {
	var problems []string
	if m.APIVersion != ManifestAPIVersion {
		problems = append(problems, fmt.Sprintf("api_version must be %q", ManifestAPIVersion))
	}
	if m.Kind != "Connector" {
		problems = append(problems, `kind must be "Connector"`)
	}
	if m.ID == "" {
		problems = append(problems, "id is required")
	}
	if m.Version == "" {
		problems = append(problems, "version is required")
	}
	if len(m.ResourceTypes) == 0 {
		problems = append(problems, "at least one resource_type is required")
	}
	problems = append(problems, validateConfigSchema(m.Config)...)
	problems = append(problems, validateManifestOperations(m.Operations)...)
	if len(problems) > 0 {
		return fmt.Errorf("connector manifest %q invalid: %s", m.ID, strings.Join(problems, "; "))
	}
	return nil
}

func validateConfigSchema(schema ConfigSchema) []string {
	var problems []string
	seenFields := make(map[string]bool)
	for _, field := range schema.Fields {
		if field.Name == "" {
			problems = append(problems, "config field name is required")
		}
		if field.Type == "" {
			problems = append(problems, fmt.Sprintf("config field %q type is required", field.Name))
		}
		if field.Name != "" {
			if seenFields[field.Name] {
				problems = append(problems, fmt.Sprintf("duplicate config field %q", field.Name))
			}
			seenFields[field.Name] = true
		}
	}

	seenSecrets := make(map[string]bool)
	for _, secret := range schema.Secrets {
		if secret.Name == "" {
			problems = append(problems, "secret name is required")
		}
		if secret.Name != "" {
			if seenSecrets[secret.Name] {
				problems = append(problems, fmt.Sprintf("duplicate secret %q", secret.Name))
			}
			seenSecrets[secret.Name] = true
		}
		if secret.Env == "" && !secret.Required {
			problems = append(problems, fmt.Sprintf("secret %q should declare env fallback or required=true", secret.Name))
		}
		// The host resolves a plugin's declared secrets and hands them to the
		// plugin in the Init config map keyed by secret name. A config field
		// sharing that name would make which value wins depend on map ordering,
		// so it is rejected at the manifest rather than discovered at runtime.
		if secret.Name != "" && seenFields[secret.Name] {
			problems = append(problems, fmt.Sprintf("secret %q collides with a config field of the same name", secret.Name))
		}
	}
	return problems
}

func validateManifestOperations(ops []ManifestOperation) []string {
	var problems []string
	if len(ops) == 0 {
		return []string{"at least one operation is required"}
	}
	seen := make(map[string]bool)
	for _, op := range ops {
		if op.Name == "" {
			problems = append(problems, "operation name is required")
			continue
		}
		if seen[op.Name] {
			problems = append(problems, fmt.Sprintf("duplicate operation %q", op.Name))
		}
		seen[op.Name] = true
		if op.InputSchema == nil {
			problems = append(problems, fmt.Sprintf("operation %q input_schema is required", op.Name))
		}
		if op.Destructive && !op.RequiresAck {
			problems = append(problems, fmt.Sprintf("destructive operation %q requires_ack must be true", op.Name))
		}
	}
	return problems
}

func cloneSchema(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchema(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

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
// The contract fields match Operation's; see contract.go.
type ManifestOperation struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Examples    []string       `json:"examples,omitempty" yaml:"examples,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`

	// Effect is the operation's effect class. A plugin operation without one
	// is a contract gap: the host treats it as exec — acknowledgment on every
	// call — and reports the gap, rather than refusing the plugin.
	Effect     Effect           `json:"effect,omitempty" yaml:"effect,omitempty"`
	Reversible bool             `json:"reversible,omitempty" yaml:"reversible,omitempty"`
	Target     TargetDescriptor `json:"target,omitempty" yaml:"target,omitempty"`
	// Preview is where the dry-run preview comes from. A plugin's preview is
	// its claim; the host does not verify it (Decision 7).
	Preview PreviewKind `json:"preview,omitempty" yaml:"preview,omitempty"`
	Output  OutputKind  `json:"output,omitempty" yaml:"output,omitempty"`
	Cost    Cost        `json:"cost,omitempty" yaml:"cost,omitempty"`
	LocalFS LocalFS     `json:"local_fs,omitempty" yaml:"local_fs,omitempty"`

	// Destructive, SupportsDry and RequiresAck are the pre-contract fields.
	// With Effect declared, the host derives all three from the contract and
	// ignores what is written here. Without it, Destructive still refuses the
	// operation to a dev install, and SupportsDry still declares a plugin
	// preview. RequiresAck is never read: an operation without an effect needs
	// acknowledgment regardless.
	Destructive bool `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	SupportsDry bool `json:"supports_dry,omitempty" yaml:"supports_dry,omitempty"`
	RequiresAck bool `json:"requires_ack,omitempty" yaml:"requires_ack,omitempty"`
}

// EffectiveEffect is the effect the host enforces: the declared one, or exec
// for an operation that declares none.
func (op ManifestOperation) EffectiveEffect() Effect {
	if op.Effect == "" {
		return EffectExec
	}
	return op.Effect
}

// IsDestructive reports whether the operation is destructive by its contract,
// or, for a manifest with no effect, by its pre-contract flag.
func (op ManifestOperation) IsDestructive() bool {
	if op.Effect != "" {
		return op.Effect == EffectDestructive
	}
	return op.Destructive
}

// EffectivePreview is the preview the host honors: the declared one, or for a
// manifest with no preview, a plugin preview when it declares supports_dry.
func (op ManifestOperation) EffectivePreview() PreviewKind {
	switch {
	case op.Preview != "":
		return op.Preview
	case op.SupportsDry:
		return PreviewPlugin
	default:
		return PreviewNone
	}
}

// ManifestFromDefinition converts build-time connector metadata into the
// Cerberus manifest shape expected by plugin adapters. The pre-contract flags
// are written derived from the contract, so a host that predates it still
// gates what this one gates.
func ManifestFromDefinition(def Definition) Manifest {
	ops := make([]ManifestOperation, 0, len(def.Operations))
	for _, op := range def.Operations {
		op = op.Finalize()
		ops = append(ops, ManifestOperation{
			Name:        op.Name,
			Description: op.Description,
			Examples:    append([]string(nil), op.Examples...),
			InputSchema: cloneSchema(op.InputSchema),
			Effect:      op.Effect,
			Reversible:  op.Reversible,
			Target:      cloneTarget(op.Target),
			Preview:     op.Preview,
			Output:      op.Output,
			Cost:        op.Cost,
			LocalFS:     op.LocalFS,
			Destructive: op.RequiresAck,
			SupportsDry: op.SupportsDry,
			RequiresAck: op.RequiresAck,
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
// Each operation carries its effective contract: a gap reads as exec.
func DefinitionFromManifest(manifest Manifest) Definition {
	ops := make([]Operation, 0, len(manifest.Operations))
	for _, op := range manifest.Operations {
		ops = append(ops, op.Operation())
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

// Operation is the operation's effective contract, finalized: a gap reads as
// exec, and its key table comes from its input schema.
func (op ManifestOperation) Operation() Operation {
	return Operation{
		Name:        op.Name,
		Description: op.Description,
		Examples:    append([]string(nil), op.Examples...),
		InputSchema: cloneSchema(op.InputSchema),
		Effect:      op.EffectiveEffect(),
		Reversible:  op.Reversible,
		Target:      cloneTarget(op.Target),
		Preview:     op.EffectivePreview(),
		Output:      op.Output,
		Cost:        op.Cost,
		LocalFS:     op.LocalFS,
	}.Finalize()
}

// ContractGaps lists what the manifest leaves undeclared, and how the host
// reads each gap. A gap is not a refusal: the plugin loads, and the gap
// always fails toward the stricter reading.
func (m Manifest) ContractGaps() []string {
	gaps := []string{}
	for _, op := range m.Operations {
		if op.Effect == "" {
			gaps = append(gaps, fmt.Sprintf("operation %q declares no effect: treated as exec, so every call needs acknowledgment, until the plugin declares one", op.Name))
		}
	}
	return gaps
}

func cloneTarget(t TargetDescriptor) TargetDescriptor {
	t.From = append([]string(nil), t.From...)
	return t
}

// Validate checks the manifest fields that Cerberus needs before accepting a
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
		problems = append(problems, validateManifestContract(op)...)
	}
	return problems
}

// validateManifestContract checks the contract values a plugin declares. An
// absent field is a gap (ContractGaps), not a problem; a value outside the
// vocabulary is a problem, because the host could only guess what it means.
func validateManifestContract(op ManifestOperation) []string {
	var problems []string
	if op.Effect != "" && !op.Effect.Valid() {
		problems = append(problems, fmt.Sprintf("operation %q effect %q is not one of %s", op.Name, op.Effect, joinKinds(Effects)))
	}
	if op.Preview != "" && !contains(previewKinds, op.Preview) {
		problems = append(problems, fmt.Sprintf("operation %q preview %q is not one of %s", op.Name, op.Preview, joinKinds(previewKinds)))
	}
	if op.Output != "" && !contains(outputKinds, op.Output) {
		problems = append(problems, fmt.Sprintf("operation %q output %q is not one of %s", op.Name, op.Output, joinKinds(outputKinds)))
	}
	if op.Cost != "" && !contains(costs, op.Cost) {
		problems = append(problems, fmt.Sprintf("operation %q cost %q is not one of %s", op.Name, op.Cost, joinKinds(costs)))
	}
	if op.LocalFS != "" && !contains(localFSKinds, op.LocalFS) {
		problems = append(problems, fmt.Sprintf("operation %q local_fs %q is not one of %s", op.Name, op.LocalFS, joinKinds(localFSKinds)))
	}
	// Pre-contract rule, kept for manifests without an effect: an older host
	// gated on both flags, so dropping requires_ack would run the operation
	// unacknowledged there.
	if op.Effect == "" && op.Destructive && !op.RequiresAck {
		problems = append(problems, fmt.Sprintf("destructive operation %q requires_ack must be true", op.Name))
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

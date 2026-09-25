package connector

import (
	"context"

	"github.com/hollis-labs/cerberus/pkg/resource"
)

// Connector manages resources of a specific type via a specific provider.
type Connector interface {
	ID() string
	ResourceTypes() []string
	Create(ctx context.Context, res *resource.Resource) error
	Start(ctx context.Context, res *resource.Resource) error
	Stop(ctx context.Context, res *resource.Resource) error
	Destroy(ctx context.Context, res *resource.Resource) error
	Status(ctx context.Context, res *resource.Resource) (resource.State, error)
	Capabilities() Capabilities
}

// Describer is implemented by connectors that expose discovery metadata.
// It is intentionally separate from Connector so existing connectors can adopt
// metadata incrementally.
type Describer interface {
	Definition() Definition
}

// Capabilities declares what operations a connector supports.
type Capabilities struct {
	CanCreate  bool `json:"can_create" yaml:"can_create"`
	CanDestroy bool `json:"can_destroy" yaml:"can_destroy"`
	CanBuild   bool `json:"can_build" yaml:"can_build"`
	CanLogs    bool `json:"can_logs" yaml:"can_logs"`
	CanHealth  bool `json:"can_health" yaml:"can_health"`
}

// Definition describes a connector for build-time registration, API discovery,
// and future plugin manifest generation.
type Definition struct {
	ID            string       `json:"id" yaml:"id"`
	Version       string       `json:"version" yaml:"version"`
	ResourceTypes []string     `json:"resource_types" yaml:"resource_types"`
	Capabilities  Capabilities `json:"capabilities" yaml:"capabilities"`
	Config        ConfigSchema `json:"config" yaml:"config"`
	Operations    []Operation  `json:"operations" yaml:"operations"`
}

// ConfigSchema declares user-provided configuration and secret requirements.
type ConfigSchema struct {
	Fields  []ConfigField       `json:"fields,omitempty" yaml:"fields,omitempty"`
	Secrets []SecretRequirement `json:"secrets,omitempty" yaml:"secrets,omitempty"`
}

// ConfigField describes a single connector configuration value.
type ConfigField struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Default     any    `json:"default,omitempty" yaml:"default,omitempty"`
}

// SecretRequirement describes a secret needed by a connector.
type SecretRequirement struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Env         string `json:"env,omitempty" yaml:"env,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	// Kind is what the value is. Unset, it is a credential. A value that is
	// read through the secret chain but is not a credential — a key file's
	// path, a team slug, an account name — declares so, and the host does
	// not value-redact it: redaction would cut it out of the very error or
	// output that has to show it.
	Kind SecretKind `json:"kind,omitempty" yaml:"kind,omitempty"`
}

// SecretKind is what a declared secret's value is.
type SecretKind string

const (
	// SecretKindCredential is a credential: the default, and value-redacted
	// from every surface the operation's text reaches.
	SecretKindCredential SecretKind = "credential"
	// SecretKindPath is the path to a credential file, such as an SSH key.
	// It is guidance ("reading SSH key <path>: no such file"), not the
	// credential.
	SecretKindPath SecretKind = "path"
	// SecretKindName is a name kept beside credentials, such as a team
	// slug, an account user or an allow-listed IP, that the operation's
	// output names in the open.
	SecretKindName SecretKind = "name"
)

// IsCredential reports whether the secret's value is a credential, and so
// is value-redacted.
func (r SecretRequirement) IsCredential() bool {
	return r.Kind == "" || r.Kind == SecretKindCredential
}

// Operation describes a connector action exposed through CLI, API, MCP, or GUI
// adapters, with its contract (see contract.go).
//
// An author declares the contract fields and Inputs. InputSchema,
// Destructive, SupportsDry and RequiresAck are derived by Finalize and are
// never set by hand: a hand-set flag is how an operation's MCP hint, ack gate
// and discovery came to disagree.
type Operation struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Examples    []string `json:"examples,omitempty" yaml:"examples,omitempty"`

	Effect     Effect           `json:"effect" yaml:"effect"`
	Reversible bool             `json:"reversible" yaml:"reversible"`
	Target     TargetDescriptor `json:"target" yaml:"target"`
	Preview    PreviewKind      `json:"preview" yaml:"preview"`
	Output     OutputKind       `json:"output" yaml:"output"`
	Cost       Cost             `json:"cost" yaml:"cost"`
	LocalFS    LocalFS          `json:"local_fs" yaml:"local_fs"`
	// EffectUndeclared marks an operation whose plugin manifest declares no
	// effect, so Effect is the host's reading of the gap (exec). Policy
	// reads it: under the permissive posture the gap is evaluated as write.
	// The contract, and every gate derived from Effect, are unchanged.
	EffectUndeclared bool `json:"effect_undeclared,omitempty" yaml:"effect_undeclared,omitempty"`

	// Inputs is the operation's key table: every config key it accepts, and
	// who may send it. The admin lane checks a caller's config against it
	// before resolving anything; InputSchema is built from it.
	Inputs []Input `json:"-" yaml:"-"`
	// OneOf lists groups of inputs of which at least one must be present,
	// for operations that take a target under any of several keys.
	OneOf [][]string `json:"one_of,omitempty" yaml:"one_of,omitempty"`
	// InputsOpen is set for a plugin schema that does not close its
	// properties: undeclared keys are then passed through, not refused.
	InputsOpen bool `json:"-" yaml:"-"`

	// Derived by Finalize.
	InputSchema map[string]any `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	Destructive bool           `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	SupportsDry bool           `json:"supports_dry,omitempty" yaml:"supports_dry,omitempty"`
	RequiresAck bool           `json:"requires_ack,omitempty" yaml:"requires_ack,omitempty"`

	// Which of Inputs and InputSchema the author declared, so Finalize
	// derives the other the same way every time.
	schemaIsSource  bool
	inputsAreSource bool
}

// Operation returns the named operation, finalized.
func (d Definition) Operation(name string) (Operation, bool) {
	for _, op := range d.Operations {
		if op.Name == name {
			return op.Finalize(), true
		}
	}
	return Operation{}, false
}

// ObjectSchema returns a minimal JSON object schema for connector operation
// metadata.
func ObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// StringSchema returns a JSON string schema with a description.
func StringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

// IntegerSchema returns a JSON integer schema with a description.
func IntegerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

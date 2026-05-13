package connector

import (
	"context"

	"github.com/chrispian/cerberus/pkg/resource"
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
}

// Operation describes a connector action exposed through CLI, API, MCP, or GUI
// adapters.
type Operation struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Examples    []string       `json:"examples,omitempty" yaml:"examples,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	Destructive bool           `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	SupportsDry bool           `json:"supports_dry,omitempty" yaml:"supports_dry,omitempty"`
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

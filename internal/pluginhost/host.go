package pluginhost

import (
	"context"

	contract "github.com/chrispian/cerberus/pkg/connector"
)

// Host is the narrow boundary Cerberus will adapt to plugin-sdk subprocesses.
// The initial interface deliberately mirrors the external connector service
// without committing the rest of Cerberus to plugin-sdk types.
type Host interface {
	Install(ctx context.Context, source string) (InstalledPlugin, error)
	Load(ctx context.Context, id string) error
	Unload(ctx context.Context, id string) error
	Health(ctx context.Context, id string) (Health, error)
	ExecuteOperation(ctx context.Context, args OperationArgs) (OperationResult, error)
}

type InstalledPlugin struct {
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Path     string            `json:"path"`
	Trust    TrustDecision     `json:"trust"`
	Spec     PluginYAML        `json:"spec"`
	Manifest contract.Manifest `json:"manifest"`
}

type Health struct {
	ID      string `json:"id"`
	Loaded  bool   `json:"loaded"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

type OperationArgs struct {
	Connector    string         `json:"connector"`
	Operation    string         `json:"operation"`
	Config       map[string]any `json:"config,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
}

type OperationResult struct {
	Connector string `json:"connector"`
	Operation string `json:"operation"`
	Data      any    `json:"data"`
}

package pluginhost

import (
	"context"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
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
	Origin   InstallOrigin     `json:"origin"`
	Spec     PluginYAML        `json:"spec"`
	Manifest contract.Manifest `json:"manifest"`
	// EntrypointSHA256 fingerprints the entrypoint binary, for the audit
	// record. What load compares is BundleDigest, which covers it.
	EntrypointSHA256 string `json:"entrypoint_sha256,omitempty"`

	// BundleDigest is the digest of the bundle the operator accepted in its
	// install review. Load recomputes it from Path and refuses a plugin that
	// no longer matches (ChangedError). It is change detection, not a trust
	// signal: it says the bundle is the one reviewed, not who built it.
	BundleDigest string `json:"bundle_digest,omitempty"`
	// ReviewPending marks a plugin installed before install review existed.
	// It keeps loading, unchecked, until the operator reviews it; its
	// previews are not accepted, so its dry runs still need acknowledgment.
	ReviewPending bool `json:"review_pending,omitempty"`

	// Granted is what the host allowed of Spec.Capabilities, decided once at
	// load and used for two things that must not disagree: the environment the
	// subprocess is launched with, and the granted list the plugin is told
	// about over Init. Computing it twice is how those two drift.
	Granted []string `json:"granted,omitempty"`
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

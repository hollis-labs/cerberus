// Package plan is what an approval binds to (I6, P3-2): a description of
// exactly what an operation would do, and its hash. An approval records the
// plan hash when it is asked for; the call that uses it recomputes the plan
// the same way and must hash equal, or it is refused as plan_stale.
//
// Each lane builds its plan with one function, used both when the approval
// is requested and when it is consumed, so the two cannot drift. A plan
// never contains a credential value: arguments appear as their keyed digest,
// environment variables by name only.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// Version is the plan recipe. Changing what a lane puts in its plan, or how
// it is encoded, bumps it, so an approval taken under one recipe is stale
// under another rather than matching by accident.
const Version = 1

// Lanes.
const (
	LaneAdmin         = "admin"
	LaneDeployProfile = "deploy_profile"
	LaneResource      = "resource"
	LanePipeline      = "pipeline"
)

// Plan is what an operation would do.
type Plan struct {
	V         int    `json:"v"`
	Lane      string `json:"lane"`
	Connector string `json:"connector"`
	Operation string `json:"operation"`
	Effect    string `json:"effect,omitempty"`
	// Target is the resolved target with its labels, as the audit record
	// names it: a relabelled target is a different plan.
	Target audit.Target `json:"target"`
	// ArgsDigest is the audit log's keyed digest of the arguments, never
	// the arguments.
	ArgsDigest string `json:"args_digest"`

	// Preview is the operation's dry-run preview, where it has one, and
	// PreviewKind where it came from (host, server, plugin).
	PreviewKind string          `json:"preview_kind,omitempty"`
	Preview     json.RawMessage `json:"preview,omitempty"`

	// For a plugin: which binary and which config would run.
	PluginEntrypointSHA256 string `json:"plugin_entrypoint_sha256,omitempty"`
	PluginConfigSHA256     string `json:"plugin_config_sha256,omitempty"`

	// Steps are what would run, for a lane that runs commands.
	Steps []Step `json:"steps,omitempty"`
	// Digests name the definitions the plan was built from (a profile, a
	// resource spec, a rendered plist).
	Digests map[string]string `json:"digests,omitempty"`
	// Source is the checkout a lane builds or deploys from.
	Source *Source `json:"source,omitempty"`
	// State is the observed state the operation would change.
	State string `json:"state,omitempty"`
}

// Step is one command a plan would run. Env names variables, never values.
type Step struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// Source is a checkout's commit and whether it has uncommitted changes.
type Source struct {
	Path  string `json:"path"`
	HEAD  string `json:"head"`
	Dirty bool   `json:"dirty"`
}

// Canonical is v as canonical JSON: keys sorted at every level (encoding/json
// sorts map keys; a struct is decoded into maps first), no insignificant
// whitespace.
func Canonical(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// Hash is the plan's hash: sha256 over its canonical JSON.
func (p Plan) Hash() (string, error) {
	p.V = Version
	data, err := Canonical(p)
	if err != nil {
		return "", fmt.Errorf("plan: encode: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// WithPreview sets the plan's preview from any DTO, canonicalized so that
// field order in the DTO cannot change the hash.
func (p *Plan) WithPreview(kind string, preview any) error {
	if preview == nil {
		return nil
	}
	data, err := Canonical(preview)
	if err != nil {
		return fmt.Errorf("plan: encode preview: %w", err)
	}
	p.PreviewKind, p.Preview = kind, data
	return nil
}

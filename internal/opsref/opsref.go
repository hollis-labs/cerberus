// Package opsref resolves the Ops identifiers a deploy carries: the Ops item
// slug (ops_id), the canonical workstream id (workstream_id) and the Torque
// task. They are resolved separately, in this order for each: an explicit flag,
// the caller's environment, the configured mapping (the resource's entry, then
// its project's) for ops_id and workstream_id, else empty. A task has no
// mapping. Nothing here fails: an unknown id is simply empty, because a deploy
// is never refused for a missing id (the Ops service files it as unlinked).
package opsref

import "strings"

// Environment variables a Tether session sets, and the one a caller may set for
// the Ops slug.
const (
	EnvOpsID       = "OPS_ID"
	EnvWorkstream  = "TETHER_WORKSTREAM_ID"
	EnvTask        = "TETHER_TASK_ID"
	defaultActor   = "msg://agent/agent-mux/agt_azos2rzing"
	defaultEnd     = "http://127.0.0.1:8994/mcp"
	defaultToken   = "/var/lib/fabric/tether/run/operator.token" //nolint:gosec // G101: a file path, not a credential
	defaultTool    = "status_post"
	defaultTimeout = 5
	defaultRetries = 2
)

// MaxRetries caps ops.retries.
const MaxRetries = 10

// Refs are the identifiers, each possibly empty.
type Refs struct {
	OpsID        string `json:"ops_id,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	Task         string `json:"task,omitempty"`
}

// Empty reports whether no identifier is known.
func (r Refs) Empty() bool { return r == Refs{} }

// Clean trims whitespace from each identifier.
func (r Refs) Clean() Refs {
	return Refs{strings.TrimSpace(r.OpsID), strings.TrimSpace(r.WorkstreamID), strings.TrimSpace(r.Task)}
}

// FromEnv reads the caller's environment through getenv.
func FromEnv(getenv func(string) string) Refs {
	return Refs{getenv(EnvOpsID), getenv(EnvWorkstream), getenv(EnvTask)}.Clean()
}

// Fill returns r with each empty identifier taken from fallback.
func (r Refs) Fill(fallback Refs) Refs {
	r, fallback = r.Clean(), fallback.Clean()
	if r.OpsID == "" {
		r.OpsID = fallback.OpsID
	}
	if r.WorkstreamID == "" {
		r.WorkstreamID = fallback.WorkstreamID
	}
	if r.Task == "" {
		r.Task = fallback.Task
	}
	return r
}

// Entry is one mapping row: the identifiers a resource or project deploys under.
type Entry struct {
	OpsID        string `yaml:"ops_id"`
	WorkstreamID string `yaml:"workstream_id"`
}

// Mapping maps resource ids and project ids to Ops identifiers.
type Mapping struct {
	Resources map[string]Entry `yaml:"resources"`
	Projects  map[string]Entry `yaml:"projects"`
}

// Lookup is the mapped identifiers for a resource: its own entry first, then its
// project's, each identifier on its own (a resource entry that names only an
// ops_id still takes the project's workstream_id).
func (m Mapping) Lookup(resource, project string) Refs {
	var out Refs
	for _, e := range []Entry{m.Resources[resource], m.Projects[project]} {
		out = out.Fill(Refs{OpsID: e.OpsID, WorkstreamID: e.WorkstreamID})
	}
	return out
}

// Resolve applies the precedence: flags, then env, then the mapping.
func Resolve(flags, env Refs, m Mapping, resource, project string) Refs {
	return flags.Fill(env).Fill(m.Lookup(resource, project))
}

// Settings is the `ops:` section of ~/.cerberus/events.yaml. The sink is off
// unless Enabled.
type Settings struct {
	Enabled   bool   `yaml:"enabled"`
	Endpoint  string `yaml:"endpoint"`
	TokenFile string `yaml:"token_file"`
	Actor     string `yaml:"actor"`
	Tool      string `yaml:"tool"`
	// TimeoutSeconds bounds each attempt; Retries is the number of attempts
	// after the first.
	TimeoutSeconds int     `yaml:"timeout"`
	Retries        *int    `yaml:"retries"`
	Map            Mapping `yaml:"map"`
}

// WithDefaults fills the unset fields.
func (s Settings) WithDefaults() Settings {
	if s.Endpoint == "" {
		s.Endpoint = defaultEnd
	}
	if s.TokenFile == "" {
		s.TokenFile = defaultToken
	}
	if s.Actor == "" {
		s.Actor = defaultActor
	}
	if s.Tool == "" {
		s.Tool = defaultTool
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = defaultTimeout
	}
	// Retries is clamped to 0..MaxRetries; a negative or absent value is not a
	// reason to skip the first attempt or to retry without bound.
	r := defaultRetries
	if s.Retries != nil {
		r = min(max(*s.Retries, 0), MaxRetries)
	}
	s.Retries = &r
	return s
}

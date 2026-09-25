package config

import "github.com/hollis-labs/cerberus/internal/target"

// ConfigV2 is the next-generation config format for Cerberus.
// It introduces projects and resources as first-class concepts,
// enabling multi-connector support (local, cloud, container, etc.).
type ConfigV2 struct {
	Version   int           `yaml:"version"`
	Build     *BuildConfig  `yaml:"build,omitempty"`
	Projects  []ProjectDef  `yaml:"projects,omitempty"`
	Resources []ResourceDef `yaml:"resources,omitempty"`
	Pipelines []PipelineDef `yaml:"pipelines,omitempty"`
}

// BuildConfig carries global defaults for build-time behavior. Fields use
// pointer types so absence in YAML is distinguishable from explicit zero,
// letting normalize-time defaulting fill in load-bearing values without
// clobbering an operator's explicit override.
type BuildConfig struct {
	// InstallAfterBuild controls whether `cerberus resource deploy` chains
	// a `make install` after a successful `make build`. Default true when
	// absent; resources may override per-resource, and the CLI may override
	// per-invocation. Precedence: CLI > resource > global > built-in true.
	InstallAfterBuild *bool `yaml:"install_after_build,omitempty"`
}

// InstallAfterBuildDefault returns the resolved global default for the
// install-after-build switch, falling back to true when the config has not
// set it explicitly. Callers should treat this as the layer-3 default in
// the precedence chain.
func (c *ConfigV2) InstallAfterBuildDefault() bool {
	if c == nil || c.Build == nil || c.Build.InstallAfterBuild == nil {
		return true
	}
	return *c.Build.InstallAfterBuild
}

// NormalizeV2 applies home-path expansion and default-filling to a
// ConfigV2. LoadUnified runs this over a single-file config; the
// registry resolver runs it over a ConfigV2 assembled from many
// app-owned project configs. Safe to call more than once — path
// expansion and default-filling are both idempotent.
func NormalizeV2(cfg *ConfigV2) {
	normalizeV2Config(cfg)
}

// ProjectDef groups related resources under a logical project.
//
// ID is the portfolio-wide project slug — the same string Cerberus uses
// as its registry key, Tesseract uses as a namespace segment, and
// agent-setup uses as a project-template basename. It is the value other
// systems join on, so it is validated rather than merely required; see
// ValidateProjectConfig in internal/registry.
type ProjectDef struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Capabilities and Links are the portable props the local control
	// plane reads. Shapes are copied from Tether's registry model so its
	// Cerberus bootstrap maps across with no translation layer.
	Capabilities []string `yaml:"capabilities,omitempty"`
	Links        []Link   `yaml:"links,omitempty"`
}

// Link is a typed pointer from a project to something outside it — its
// repo, its docs, the org that owns it.
//
// Kind is deliberately free-form rather than an enum. That is the same
// call Tether's ADR 0041 made (D16): a closed vocabulary means every new
// relation needs a coordinated schema change in every reader, and the
// blessed v1 kinds (repo, docs, pipeline, owned_by, requires_secret, …)
// are a convention to document, not a constraint to enforce.
type Link struct {
	Kind   string `yaml:"kind" json:"kind"`
	Target string `yaml:"target" json:"target"`
}

// ResourceDef represents a managed resource (process, server, container, etc.).
type ResourceDef struct {
	ID        string         `yaml:"id"`
	Name      string         `yaml:"name"`
	Type      string         `yaml:"type"`      // "process", "server", "container", etc.
	Project   string         `yaml:"project"`   // project ID
	Connector string         `yaml:"connector"` // "local", "digitalocean", "github", etc.
	Config    map[string]any `yaml:"config"`    // connector-specific config
	Tags      []string       `yaml:"tags,omitempty"`
	DependsOn []string       `yaml:"depends_on,omitempty"`

	// Env, Owner and Admin label the resource for policy (Decision 18):
	// where it runs, who provisions and owns it, and who administers it day
	// to day, optionally per sub-target kind. Unset is unknown, read as
	// strictly as possible (Decision 17). Operations on the resource, and on
	// anything under it, inherit these.
	Env   target.Env   `yaml:"env,omitempty"`
	Owner string       `yaml:"owner,omitempty"`
	Admin target.Admin `yaml:"admin,omitempty"`
}

// TargetLabels are the resource's policy labels.
func (r ResourceDef) TargetLabels() target.ResourceLabels {
	return target.ResourceLabels{ID: r.ID, Labels: target.Labels{Env: r.Env, Owner: r.Owner, Admin: r.Admin, Tags: append([]string(nil), r.Tags...)}}
}

// PipelineDef defines a multi-step workflow in config.
type PipelineDef struct {
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Stages      []StageDef `yaml:"stages"`
}

// StageDef defines a single stage within a pipeline.
type StageDef struct {
	Name      string      `yaml:"name"`
	DependsOn []string    `yaml:"depends_on,omitempty"`
	Actions   []ActionDef `yaml:"actions"`
}

// ActionDef defines a single action within a stage.
type ActionDef struct {
	Type     string `yaml:"type"`               // "build", "deploy", "start", "stop", "health_wait", "shell"
	Resource string `yaml:"resource,omitempty"` // resource ID (for build/deploy/start/stop/health_wait)
	Command  string `yaml:"command,omitempty"`  // shell command (for shell type)
	Dir      string `yaml:"dir,omitempty"`      // working directory (for shell type)
	Timeout  string `yaml:"timeout,omitempty"`  // duration string (for health_wait)
}

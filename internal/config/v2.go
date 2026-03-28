package config

// ConfigV2 is the next-generation config format for Cerberus.
// It introduces projects and resources as first-class concepts,
// enabling multi-connector support (local, cloud, container, etc.).
type ConfigV2 struct {
	Version   int           `yaml:"version"`
	Projects  []ProjectDef  `yaml:"projects,omitempty"`
	Resources []ResourceDef `yaml:"resources,omitempty"`
	Pipelines []PipelineDef `yaml:"pipelines,omitempty"`
}

// ProjectDef groups related resources under a logical project.
type ProjectDef struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
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
	Type     string `yaml:"type"`               // "build", "start", "stop", "health_wait", "shell"
	Resource string `yaml:"resource,omitempty"` // resource ID (for build/start/stop/health_wait)
	Command  string `yaml:"command,omitempty"`  // shell command (for shell type)
	Dir      string `yaml:"dir,omitempty"`      // working directory (for shell type)
	Timeout  string `yaml:"timeout,omitempty"`  // duration string (for health_wait)
}

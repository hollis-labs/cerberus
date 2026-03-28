package domain

import "time"

// ResourceType classifies what kind of infrastructure a Resource represents.
type ResourceType string

const (
	ResourceProcess   ResourceType = "process"
	ResourceServer    ResourceType = "server"
	ResourceContainer ResourceType = "container"
	ResourceDomain    ResourceType = "domain"
	ResourcePipeline  ResourceType = "pipeline"
	ResourceRepo      ResourceType = "repository"
)

// Resource is the universal unit of managed infrastructure.
// Everything Cerberus manages — a local process, a cloud server, a DNS record,
// a Docker container — is a Resource with a connector that knows how to operate it.
type Resource struct {
	ID        string         `json:"id" yaml:"id"`
	Name      string         `json:"name" yaml:"name"`
	Type      ResourceType   `json:"type" yaml:"type"`
	ProjectID string         `json:"project_id" yaml:"project_id"`
	Connector string         `json:"connector" yaml:"connector"`
	Config    map[string]any `json:"config" yaml:"config"`
	Tags      []string       `json:"tags,omitempty" yaml:"tags,omitempty"`
	DependsOn []string       `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	CreatedAt time.Time      `json:"created_at" yaml:"-"`
	UpdatedAt time.Time      `json:"updated_at" yaml:"-"`
}

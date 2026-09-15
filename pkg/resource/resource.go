package resource

import "time"

// Type classifies what kind of infrastructure a Resource represents.
type Type string

const (
	Process   Type = "process"
	Server    Type = "server"
	Container Type = "container"
	Domain    Type = "domain"
	Repo      Type = "repository"
)

// State represents the current known state of a Resource.
type State string

const (
	StateStopped   State = "stopped"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
	StateBuilding  State = "building"
	StateFailed    State = "failed"
	StateDestroyed State = "destroyed"
	StateUnknown   State = "unknown"
)

// Resource is the universal unit of managed infrastructure.
// Everything Cerberus manages - a local process, a cloud server, a DNS record,
// a Docker container - is a Resource with a connector that knows how to
// operate it.
type Resource struct {
	ID        string         `json:"id" yaml:"id"`
	Name      string         `json:"name" yaml:"name"`
	Type      Type           `json:"type" yaml:"type"`
	ProjectID string         `json:"project_id" yaml:"project_id"`
	Connector string         `json:"connector" yaml:"connector"`
	Config    map[string]any `json:"config" yaml:"config"`
	Tags      []string       `json:"tags,omitempty" yaml:"tags,omitempty"`
	DependsOn []string       `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	CreatedAt time.Time      `json:"created_at" yaml:"-"`
	UpdatedAt time.Time      `json:"updated_at" yaml:"-"`
}

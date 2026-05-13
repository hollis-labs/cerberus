package domain

import "github.com/chrispian/cerberus/pkg/resource"

// ResourceType classifies what kind of infrastructure a Resource represents.
type ResourceType = resource.Type

const (
	ResourceProcess   = resource.Process
	ResourceServer    = resource.Server
	ResourceContainer = resource.Container
	ResourceDomain    = resource.Domain
	ResourcePipeline  = resource.Pipeline
	ResourceRepo      = resource.Repo
)

// Resource is the universal unit of managed infrastructure.
// Everything Cerberus manages — a local process, a cloud server, a DNS record,
// a Docker container — is a Resource with a connector that knows how to operate it.
type Resource = resource.Resource

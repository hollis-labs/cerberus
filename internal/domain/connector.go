package domain

import "context"

// Connector manages resources of a specific type via a specific provider.
// The local connector wraps OS process management; the DigitalOcean connector
// wraps the DO API; the Docker connector wraps the Docker daemon; etc.
type Connector interface {
	ID() string
	ResourceTypes() []string
	Create(ctx context.Context, res *Resource) error
	Start(ctx context.Context, res *Resource) error
	Stop(ctx context.Context, res *Resource) error
	Destroy(ctx context.Context, res *Resource) error
	Status(ctx context.Context, res *Resource) (State, error)
	Capabilities() ConnectorCapabilities
}

// ConnectorCapabilities declares what operations a connector supports.
type ConnectorCapabilities struct {
	CanCreate  bool
	CanDestroy bool
	CanBuild   bool
	CanLogs    bool
	CanHealth  bool
}

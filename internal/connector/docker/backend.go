package docker

import "context"

// Backend defines how the Docker connector communicates with Docker.
// Two implementations planned: CLIBackend (docker CLI) and APIBackend (Docker SDK).
// Only CLIBackend is implemented to avoid heavy SDK dependencies.
type Backend interface {
	// WithTarget returns a Backend bound to the given Docker daemon, leaving
	// the receiver unchanged. Host selection is per operation, so a single
	// connector instance must be able to serve several hosts without any call
	// mutating state another call can observe.
	WithTarget(target Target) Backend

	// ListContainers returns all running containers.
	ListContainers(ctx context.Context) ([]Container, error)

	// ContainerStatus returns the current state of a single container.
	ContainerStatus(ctx context.Context, nameOrID string) (*Container, error)

	// StartContainer starts a stopped container.
	StartContainer(ctx context.Context, nameOrID string) error

	// StopContainer stops a running container.
	StopContainer(ctx context.Context, nameOrID string) error

	// RemoveContainer removes a container.
	RemoveContainer(ctx context.Context, nameOrID string) error

	// ContainerLogs returns the last N lines of container logs.
	ContainerLogs(ctx context.Context, nameOrID string, lines int) (string, error)

	// ComposeUp starts a Compose stack in detached mode.
	ComposeUp(ctx context.Context, composeFile string) error

	// ComposeDown stops and removes a Compose stack.
	ComposeDown(ctx context.Context, composeFile string) error

	// ComposePS returns the status of services in a Compose stack.
	ComposePS(ctx context.Context, composeFile string) (*ComposeStack, error)
}

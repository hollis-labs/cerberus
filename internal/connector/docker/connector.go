package docker

import (
	"context"
	"encoding/json"
	"fmt"
)

// Connector manages Docker resources (containers, compose stacks) via the
// docker CLI. An API backend (Docker SDK) can be added later.
type Connector struct {
	backend Backend
}

// New creates a Docker connector. It uses the CLI backend (docker in PATH).
func New() (*Connector, error) {
	path, ok := DetectDocker()
	if !ok {
		return nil, fmt.Errorf("docker connector: docker CLI not found in PATH")
	}
	return &Connector{backend: newCLIBackendWithPath(path)}, nil
}

// NewWithBackend creates a Docker connector with an explicit backend.
func NewWithBackend(b Backend) *Connector {
	return &Connector{backend: b}
}

// StartContainer starts a container by name or ID.
func (c *Connector) StartContainer(ctx context.Context, nameOrID string) error {
	return c.backend.StartContainer(ctx, nameOrID)
}

// StopContainer stops a container by name or ID.
func (c *Connector) StopContainer(ctx context.Context, nameOrID string) error {
	return c.backend.StopContainer(ctx, nameOrID)
}

// RemoveContainer removes a container by name or ID.
func (c *Connector) RemoveContainer(ctx context.Context, nameOrID string) error {
	return c.backend.RemoveContainer(ctx, nameOrID)
}

// ContainerStatus returns the current state of a single container.
func (c *Connector) ContainerStatus(ctx context.Context, nameOrID string) (*Container, error) {
	return c.backend.ContainerStatus(ctx, nameOrID)
}

// ListContainers returns all running containers.
func (c *Connector) ListContainers(ctx context.Context) ([]Container, error) {
	return c.backend.ListContainers(ctx)
}

// Logs returns the last N lines of a container's logs.
func (c *Connector) Logs(ctx context.Context, nameOrID string, lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	return c.backend.ContainerLogs(ctx, nameOrID, lines)
}

// ComposeUp starts a compose stack.
func (c *Connector) ComposeUp(ctx context.Context, composeFile string) error {
	return c.backend.ComposeUp(ctx, composeFile)
}

// ComposeDown stops a compose stack.
func (c *Connector) ComposeDown(ctx context.Context, composeFile string) error {
	return c.backend.ComposeDown(ctx, composeFile)
}

// ComposePS returns the status of a compose stack.
func (c *Connector) ComposePS(ctx context.Context, composeFile string) (*ComposeStack, error) {
	return c.backend.ComposePS(ctx, composeFile)
}

// ContainersJSON returns all running containers as a JSON string (used by MCP tools).
func (c *Connector) ContainersJSON(ctx context.Context) (string, error) {
	containers, err := c.backend.ListContainers(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(containers, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LogsJSON returns container logs as a JSON string (used by MCP tools).
func (c *Connector) LogsJSON(ctx context.Context, nameOrID string, lines int) (string, error) {
	logs, err := c.Logs(ctx, nameOrID, lines)
	if err != nil {
		return "", err
	}
	result := struct {
		Container string `json:"container"`
		Lines     int    `json:"lines"`
		Output    string `json:"output"`
	}{
		Container: nameOrID,
		Lines:     lines,
		Output:    logs,
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

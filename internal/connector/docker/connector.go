package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

// Connector manages Docker resources (containers, compose stacks) via the
// docker CLI. An API backend (Docker SDK) can be added later.
type Connector struct {
	backend Backend
}

// ErrDockerNotFound reports that the docker CLI could not be located. The
// message names the fallbacks and the override because the usual cause is a
// caller with a minimal PATH (a launchd-started daemon), not a missing Docker.
var ErrDockerNotFound = fmt.Errorf(
	"docker CLI not found on PATH or in any known install location (%s); "+
		"set %s to the docker binary if it lives elsewhere",
	strings.Join(fallbackDockerPaths, ", "), DockerPathEnv)

// New creates a Docker connector using the CLI backend. It is called per
// operation via the connector registry's factory, so docker becoming available
// later — Docker Desktop starting, a PATH corrected — is picked up without a
// daemon restart.
func New() (*Connector, error) {
	path, ok := DetectDocker()
	if !ok {
		return nil, fmt.Errorf("docker connector: %w", ErrDockerNotFound)
	}
	return &Connector{backend: newCLIBackendWithPath(path)}, nil
}

// NewWithBackend creates a Docker connector with an explicit backend.
func NewWithBackend(b Backend) *Connector {
	return &Connector{backend: b}
}

func (c *Connector) ID() string              { return "docker" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Container)} }

func (c *Connector) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		CanCreate:  false,
		CanDestroy: true,
		CanBuild:   true,
		CanLogs:    true,
		CanHealth:  true,
	}
}

func Definition() contract.Definition {
	return contract.Definition{
		ID:            "docker",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Container)},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: true,
			CanBuild:   true,
			CanLogs:    true,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        "container",
					Type:        "string",
					Description: "Docker container name or ID.",
				},
				{
					Name:        "compose_file",
					Type:        "string",
					Description: "Docker Compose file path for compose-backed resources.",
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:        "list_containers",
				Description: "List running Docker containers.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:        "start",
				Description: "Start a Docker container or compose stack.",
				InputSchema: dockerResourceInputSchema(),
			},
			{
				Name:        "stop",
				Description: "Stop a Docker container or compose stack.",
				InputSchema: dockerResourceInputSchema(),
			},
			{
				Name:        "destroy",
				Description: "Remove a Docker container or stop a compose stack.",
				InputSchema: dockerResourceInputSchema(),
				Destructive: true,
			},
			{
				Name:        "logs",
				Description: "Read recent Docker container logs.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"container": contract.StringSchema("Docker container name or ID."),
					"lines":     contract.IntegerSchema("Number of log lines to return."),
				}),
			},
		},
	}
}

func (c *Connector) Definition() contract.Definition {
	return Definition()
}

func (c *Connector) Create(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("docker connector does not support Create")
}

func (c *Connector) Start(ctx context.Context, res *resource.Resource) error {
	if composeFile := dockerComposeFile(res); composeFile != "" {
		return c.ComposeUp(ctx, composeFile)
	}
	return c.StartContainer(ctx, dockerContainerName(res))
}

func (c *Connector) Stop(ctx context.Context, res *resource.Resource) error {
	if composeFile := dockerComposeFile(res); composeFile != "" {
		return c.ComposeDown(ctx, composeFile)
	}
	return c.StopContainer(ctx, dockerContainerName(res))
}

func (c *Connector) Destroy(ctx context.Context, res *resource.Resource) error {
	if composeFile := dockerComposeFile(res); composeFile != "" {
		return c.ComposeDown(ctx, composeFile)
	}
	return c.RemoveContainer(ctx, dockerContainerName(res))
}

func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	if composeFile := dockerComposeFile(res); composeFile != "" {
		stack, err := c.ComposePS(ctx, composeFile)
		if err != nil {
			return resource.StateUnknown, err
		}
		return dockerComposeState(stack), nil
	}

	container, err := c.ContainerStatus(ctx, dockerContainerName(res))
	if err != nil {
		return resource.StateUnknown, err
	}
	return dockerContainerState(container), nil
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

func dockerContainerName(res *resource.Resource) string {
	for _, key := range []string{"container", "container_id", "container_name", "name"} {
		if value, ok := res.Config[key].(string); ok && value != "" {
			return value
		}
	}
	if res.Name != "" {
		return res.Name
	}
	return res.ID
}

func dockerComposeFile(res *resource.Resource) string {
	for _, key := range []string{"compose_file", "composeFile", "file"} {
		if value, ok := res.Config[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func dockerContainerState(container *Container) resource.State {
	if container == nil {
		return resource.StateUnknown
	}
	switch container.State {
	case "running":
		return resource.StateRunning
	case "created", "restarting":
		return resource.StateStarting
	case "exited", "dead", "removing", "paused":
		return resource.StateStopped
	default:
		return resource.StateUnknown
	}
}

func dockerComposeState(stack *ComposeStack) resource.State {
	if stack == nil || len(stack.Services) == 0 {
		return resource.StateUnknown
	}

	running := 0
	for _, service := range stack.Services {
		if service.State == "running" {
			running++
		}
	}
	if running == len(stack.Services) {
		return resource.StateRunning
	}
	if running > 0 {
		return resource.StateStarting
	}
	return resource.StateStopped
}

func dockerResourceInputSchema() map[string]any {
	return contract.ObjectSchema(map[string]any{
		"container":    contract.StringSchema("Docker container name or ID."),
		"compose_file": contract.StringSchema("Docker Compose file path."),
	})
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

package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CLIBackend implements Backend by shelling out to the docker CLI.
type CLIBackend struct {
	dockerPath string
}

// DetectDocker returns the path to the docker binary and whether it was found.
func DetectDocker() (string, bool) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return "", false
	}
	return path, true
}

// NewCLIBackend creates a CLIBackend after verifying that docker is available in PATH.
func NewCLIBackend() (*CLIBackend, error) {
	path, found := DetectDocker()
	if !found {
		return nil, fmt.Errorf("docker CLI not found in PATH")
	}
	return &CLIBackend{dockerPath: path}, nil
}

// newCLIBackendWithPath creates a CLIBackend using an already-resolved docker path.
func newCLIBackendWithPath(path string) *CLIBackend {
	return &CLIBackend{dockerPath: path}
}

// --- intermediate JSON structs for docker CLI output ---

type dockerPSJSON struct {
	ID        string `json:"ID"`
	Names     string `json:"Names"`
	Image     string `json:"Image"`
	Status    string `json:"Status"`
	State     string `json:"State"`
	Ports     string `json:"Ports"`
	CreatedAt string `json:"CreatedAt"`
}

type dockerInspectJSON struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	State   struct {
		Status string `json:"Status"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports json.RawMessage `json:"Ports"`
	} `json:"NetworkSettings"`
}

type composePSJSON struct {
	Name       string `json:"Name"`
	Service    string `json:"Service"`
	State      string `json:"State"`
	Image      string `json:"Image"`
	Publishers []struct {
		URL           string `json:"URL"`
		TargetPort    int    `json:"TargetPort"`
		PublishedPort int    `json:"PublishedPort"`
		Protocol      string `json:"Protocol"`
	} `json:"Publishers"`
}

// ListContainers returns all running containers via docker ps --format json.
func (c *CLIBackend) ListContainers(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, "ps", "--format", "json", "--no-trunc")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}

	return c.parsePSOutput(out)
}

// ContainerStatus returns the current state of a single container via docker inspect.
func (c *CLIBackend) ContainerStatus(ctx context.Context, nameOrID string) (*Container, error) {
	out, err := c.run(ctx, "inspect", "--format", "json", nameOrID)
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", nameOrID, err)
	}

	var inspects []dockerInspectJSON
	if err := json.Unmarshal(out, &inspects); err != nil {
		return nil, fmt.Errorf("parsing docker inspect JSON: %w", err)
	}
	if len(inspects) == 0 {
		return nil, fmt.Errorf("container %s not found", nameOrID)
	}

	i := inspects[0]
	name := strings.TrimPrefix(i.Name, "/")
	return &Container{
		ID:        i.ID,
		Name:      name,
		Image:     i.Config.Image,
		Status:    i.State.Status,
		State:     i.State.Status,
		CreatedAt: i.Created,
	}, nil
}

// StartContainer starts a stopped container via docker start.
func (c *CLIBackend) StartContainer(ctx context.Context, nameOrID string) error {
	_, err := c.run(ctx, "start", nameOrID)
	if err != nil {
		return fmt.Errorf("docker start %s: %w", nameOrID, err)
	}
	return nil
}

// StopContainer stops a running container via docker stop.
func (c *CLIBackend) StopContainer(ctx context.Context, nameOrID string) error {
	_, err := c.run(ctx, "stop", nameOrID)
	if err != nil {
		return fmt.Errorf("docker stop %s: %w", nameOrID, err)
	}
	return nil
}

// RemoveContainer removes a container via docker rm.
func (c *CLIBackend) RemoveContainer(ctx context.Context, nameOrID string) error {
	_, err := c.run(ctx, "rm", nameOrID)
	if err != nil {
		return fmt.Errorf("docker rm %s: %w", nameOrID, err)
	}
	return nil
}

// ContainerLogs returns the last N lines of container logs via docker logs.
func (c *CLIBackend) ContainerLogs(ctx context.Context, nameOrID string, lines int) (string, error) {
	out, err := c.run(ctx, "logs", "--tail", strconv.Itoa(lines), nameOrID)
	if err != nil {
		return "", fmt.Errorf("docker logs %s: %w", nameOrID, err)
	}
	return string(out), nil
}

// ComposeUp starts a Compose stack via docker compose up -d.
func (c *CLIBackend) ComposeUp(ctx context.Context, composeFile string) error {
	_, err := c.run(ctx, "compose", "-f", composeFile, "up", "-d")
	if err != nil {
		return fmt.Errorf("docker compose up -f %s: %w", composeFile, err)
	}
	return nil
}

// ComposeDown stops and removes a Compose stack via docker compose down.
func (c *CLIBackend) ComposeDown(ctx context.Context, composeFile string) error {
	_, err := c.run(ctx, "compose", "-f", composeFile, "down")
	if err != nil {
		return fmt.Errorf("docker compose down -f %s: %w", composeFile, err)
	}
	return nil
}

// ComposePS returns the status of services in a Compose stack via docker compose ps.
func (c *CLIBackend) ComposePS(ctx context.Context, composeFile string) (*ComposeStack, error) {
	out, err := c.run(ctx, "compose", "-f", composeFile, "ps", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ps -f %s: %w", composeFile, err)
	}

	services, err := c.parseComposePSOutput(out)
	if err != nil {
		return nil, err
	}

	// Determine overall status from services
	status := "stopped"
	for _, svc := range services {
		if svc.State == "running" {
			status = "running"
			break
		}
	}

	return &ComposeStack{
		Name:       "",
		Status:     status,
		ConfigFile: composeFile,
		Services:   services,
	}, nil
}

// parsePSOutput parses the line-delimited JSON output from docker ps --format json.
func (c *CLIBackend) parsePSOutput(out []byte) ([]Container, error) {
	var containers []Container
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ps dockerPSJSON
		if err := json.Unmarshal([]byte(line), &ps); err != nil {
			return nil, fmt.Errorf("parsing docker ps JSON line: %w", err)
		}
		var ports []string
		if ps.Ports != "" {
			ports = strings.Split(ps.Ports, ", ")
		}
		containers = append(containers, Container{
			ID:        ps.ID,
			Name:      ps.Names,
			Image:     ps.Image,
			Status:    ps.Status,
			State:     ps.State,
			Ports:     ports,
			CreatedAt: ps.CreatedAt,
		})
	}
	return containers, nil
}

// parseComposePSOutput parses the JSON output from docker compose ps --format json.
func (c *CLIBackend) parseComposePSOutput(out []byte) ([]ComposeService, error) {
	var services []ComposeService

	// docker compose ps --format json can output either a JSON array or line-delimited JSON
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return services, nil
	}

	// Try JSON array first
	if strings.HasPrefix(trimmed, "[") {
		var items []composePSJSON
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, fmt.Errorf("parsing compose ps JSON array: %w", err)
		}
		for _, item := range items {
			services = append(services, composeServiceFromJSON(item))
		}
		return services, nil
	}

	// Fall back to line-delimited JSON
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item composePSJSON
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("parsing compose ps JSON line: %w", err)
		}
		services = append(services, composeServiceFromJSON(item))
	}
	return services, nil
}

func composeServiceFromJSON(item composePSJSON) ComposeService {
	var ports []string
	for _, p := range item.Publishers {
		if p.PublishedPort > 0 {
			ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", p.URL, p.PublishedPort, p.TargetPort, p.Protocol))
		}
	}
	name := item.Service
	if name == "" {
		name = item.Name
	}
	return ComposeService{
		Name:  name,
		State: item.State,
		Image: item.Image,
		Ports: ports,
	}
}

// run executes a docker subcommand and returns its stdout bytes.
func (c *CLIBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.dockerPath, args...) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok { //nolint:errorlint
			return nil, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return nil, err
	}
	return out, nil
}

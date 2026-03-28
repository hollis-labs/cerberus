package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector manages remote servers via SSH.
type Connector struct {
	secrets domain.SecretProvider
}

// New creates an SSH connector.
func New(secrets domain.SecretProvider) *Connector {
	return &Connector{secrets: secrets}
}

func (c *Connector) ID() string              { return "ssh" }
func (c *Connector) ResourceTypes() []string { return []string{"server"} }

func (c *Connector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{
		CanCreate:  false,
		CanDestroy: false,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

func (c *Connector) Create(_ context.Context, _ *domain.Resource) error {
	return nil // no-op
}

func (c *Connector) Start(_ context.Context, _ *domain.Resource) error {
	return nil // no-op — can't start a remote server via SSH
}

func (c *Connector) Stop(ctx context.Context, res *domain.Resource) error {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return fmt.Errorf("ssh stop: %w", err)
	}
	defer backend.Close() //nolint:errcheck

	result, err := backend.Exec(ctx, "sudo shutdown -h now")
	if err != nil {
		return fmt.Errorf("ssh stop: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("ssh stop: exit %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}

func (c *Connector) Destroy(_ context.Context, _ *domain.Resource) error {
	return nil // no-op
}

func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return domain.StateStopped, nil //nolint:nilerr // unreachable means stopped
	}
	defer backend.Close() //nolint:errcheck

	if err := backend.Ping(ctx); err != nil {
		return domain.StateStopped, nil //nolint:nilerr // ping failure means stopped
	}

	return domain.StateRunning, nil
}

// Exec runs an arbitrary command on the remote host described by the resource.
func (c *Connector) Exec(ctx context.Context, res *domain.Resource, command string) (*ExecResult, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("ssh exec: %w", err)
	}
	defer backend.Close() //nolint:errcheck

	return backend.Exec(ctx, command)
}

// HostStatusJSON returns the connectivity status of a remote host as JSON.
func (c *Connector) HostStatusJSON(ctx context.Context, res *domain.Resource) (string, error) {
	status := HostStatus{}

	start := time.Now()
	backend, err := c.connect(ctx, res)
	if err != nil {
		status.Reachable = false
		data, _ := json.MarshalIndent(status, "", "  ")
		return string(data), nil
	}
	defer backend.Close() //nolint:errcheck

	if pingErr := backend.Ping(ctx); pingErr != nil {
		status.Reachable = false
		data, _ := json.MarshalIndent(status, "", "  ")
		return string(data), nil
	}
	status.Latency = time.Since(start)
	status.Reachable = true

	// Try to get OS info
	result, err := backend.Exec(ctx, "uname -s")
	if err == nil && result.ExitCode == 0 {
		status.OS = result.Stdout
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal host status: %w", err)
	}
	return string(data), nil
}

// ExecJSON runs a command and returns the result as JSON.
func (c *Connector) ExecJSON(ctx context.Context, res *domain.Resource, command string) (string, error) {
	result, err := c.Exec(ctx, res, command)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal exec result: %w", err)
	}
	return string(data), nil
}

// connect creates and connects a backend for the given resource.
func (c *Connector) connect(ctx context.Context, res *domain.Resource) (Backend, error) {
	host, _ := res.Config["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("ssh resource %q missing host in config", res.ID)
	}

	port := 22
	if p, ok := res.Config["port"].(int); ok && p > 0 {
		port = p
	} else if p, ok := res.Config["port"].(float64); ok && p > 0 {
		port = int(p)
	}

	user, _ := res.Config["user"].(string)
	if user == "" {
		user = "root"
	}

	keyFile, _ := res.Config["key_file"].(string)
	if keyFile == "" && c.secrets != nil {
		// Try keychain: ssh/<resource-id>/key
		if k, err := c.secrets.Get(ctx, "ssh/"+res.ID, "key"); err == nil && k != "" {
			keyFile = k
		}
	}
	if keyFile == "" {
		// Env fallback
		keyFile = os.Getenv("CERBERUS_SSH_KEY_FILE")
	}
	if keyFile == "" {
		return nil, fmt.Errorf("ssh resource %q: no key_file in config, keychain, or CERBERUS_SSH_KEY_FILE env", res.ID)
	}

	backend := NewAPIBackend()
	if err := backend.Connect(ctx, host, port, user, keyFile); err != nil {
		return nil, err
	}
	return backend, nil
}

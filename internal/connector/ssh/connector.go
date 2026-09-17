package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

type BackendFactory func() Backend

// Connector manages remote servers via SSH.
type Connector struct {
	secrets    secret.Provider
	newBackend BackendFactory
}

// New creates an SSH connector.
func New(secrets secret.Provider) *Connector {
	return &Connector{secrets: secrets, newBackend: func() Backend { return NewAPIBackend() }}
}

func NewWithBackendFactory(secrets secret.Provider, factory BackendFactory) *Connector {
	if factory == nil {
		factory = func() Backend { return NewAPIBackend() }
	}
	return &Connector{secrets: secrets, newBackend: factory}
}

func (c *Connector) ID() string              { return "ssh" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Server)} }

func (c *Connector) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		CanCreate:  false,
		CanDestroy: false,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

func Definition() contract.Definition {
	return contract.Definition{
		ID:            "ssh",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
			CanBuild:   false,
			CanLogs:    false,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "host", Type: "string", Description: "Remote host name or IP address.", Required: true},
				{Name: "port", Type: "integer", Description: "SSH port.", Default: 22},
				{Name: "user", Type: "string", Description: "SSH username.", Default: "root"},
				{Name: "key_file", Type: "string", Description: "Private key path."},
				{Name: "known_hosts_file", Type: "string", Description: "OpenSSH known_hosts file for host key verification. Defaults to ~/.ssh/known_hosts."},
				{Name: "allow_insecure_host_key", Type: "boolean", Description: "Disable host key verification. Not recommended except for controlled local testing."},
				{Name: "command", Type: "string", Description: "Command to execute over SSH."},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        "key",
				Description: "SSH private key path resolved from ssh/<resource-id>/key when key_file is omitted.",
				Env:         "CERBERUS_SSH_KEY_FILE",
			}},
		},
		Operations: []contract.Operation{
			{Name: "status", Description: "Check SSH reachability and basic host status.", InputSchema: sshInputSchema()},
			{Name: "exec", Description: "Execute a command over SSH.", Examples: []string{"cerberus ssh exec prod-api -- 'systemctl status nginx' --dry-run", "cerberus ssh exec prod-api -- 'systemctl restart php-fpm' --ack"}, InputSchema: contract.ObjectSchema(map[string]any{
				"host":     contract.StringSchema("Remote host name or IP address."),
				"port":     contract.IntegerSchema("SSH port."),
				"user":     contract.StringSchema("SSH username."),
				"key_file": contract.StringSchema("Private key path."),
				"command":  contract.StringSchema("Command to execute."),
			}, "host", "command"), Destructive: true, SupportsDry: true},
			{Name: "put", Description: "Upload a local file to the remote host over SFTP.", Examples: []string{"cerberus ssh put prod-api ./docker-compose.yml /opt/app/docker-compose.yml --dry-run", "cerberus ssh put prod-api ./app.env /opt/app/.env --ack"}, InputSchema: contract.ObjectSchema(map[string]any{
				"host":        contract.StringSchema("Remote host name or IP address."),
				"port":        contract.IntegerSchema("SSH port."),
				"user":        contract.StringSchema("SSH username."),
				"key_file":    contract.StringSchema("Private key path."),
				"local_path":  contract.StringSchema("Local file to upload."),
				"remote_path": contract.StringSchema("Destination path on the remote host."),
			}, "host", "local_path", "remote_path"), Destructive: true, SupportsDry: true},
			{Name: "get", Description: "Download a file from the remote host over SFTP.", Examples: []string{"cerberus ssh get prod-api /etc/nginx/nginx.conf ./nginx.conf"}, InputSchema: contract.ObjectSchema(map[string]any{
				"host":        contract.StringSchema("Remote host name or IP address."),
				"port":        contract.IntegerSchema("SSH port."),
				"user":        contract.StringSchema("SSH username."),
				"key_file":    contract.StringSchema("Private key path."),
				"remote_path": contract.StringSchema("File to download from the remote host."),
				"local_path":  contract.StringSchema("Local destination path."),
			}, "host", "remote_path", "local_path")},
			{Name: "stop", Description: "Shut down the remote host via SSH.", Examples: []string{"cerberus ssh stop prod-api --dry-run", "cerberus ssh stop prod-api --ack"}, InputSchema: sshInputSchema(), Destructive: true, SupportsDry: true},
		},
	}
}

func (c *Connector) Definition() contract.Definition { return Definition() }

func sshInputSchema() map[string]any {
	return contract.ObjectSchema(map[string]any{
		"host":     contract.StringSchema("Remote host name or IP address."),
		"port":     contract.IntegerSchema("SSH port."),
		"user":     contract.StringSchema("SSH username."),
		"key_file": contract.StringSchema("Private key path."),
		"id":       contract.StringSchema("Resource ID used to resolve keychain secrets."),
		"name":     contract.StringSchema("Resource name."),
	}, "host")
}

func (c *Connector) Create(_ context.Context, _ *resource.Resource) error {
	return nil // no-op
}

func (c *Connector) Start(_ context.Context, _ *resource.Resource) error {
	return nil // no-op — can't start a remote server via SSH
}

func (c *Connector) Stop(ctx context.Context, res *resource.Resource) error {
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

func (c *Connector) Destroy(_ context.Context, _ *resource.Resource) error {
	return nil // no-op
}

func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return resource.StateStopped, nil //nolint:nilerr // unreachable means stopped
	}
	defer backend.Close() //nolint:errcheck

	if err := backend.Ping(ctx); err != nil {
		return resource.StateStopped, nil //nolint:nilerr // ping failure means stopped
	}

	return resource.StateRunning, nil
}

// Exec runs an arbitrary command on the remote host described by the resource.
func (c *Connector) Exec(ctx context.Context, res *resource.Resource, command string) (*ExecResult, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("ssh exec: %w", err)
	}
	defer backend.Close() //nolint:errcheck

	return backend.Exec(ctx, command)
}

// TransferResult reports the outcome of a file transfer.
type TransferResult struct {
	LocalPath  string `json:"local_path"`
	RemotePath string `json:"remote_path"`
	Bytes      int64  `json:"bytes"`
}

// Put uploads a local file to the remote host described by the resource.
func (c *Connector) Put(ctx context.Context, res *resource.Resource, localPath, remotePath string) (*TransferResult, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("ssh put: %w", err)
	}
	defer backend.Close() //nolint:errcheck

	written, err := backend.Put(ctx, localPath, remotePath)
	if err != nil {
		return nil, fmt.Errorf("ssh put: %w", err)
	}
	return &TransferResult{LocalPath: localPath, RemotePath: remotePath, Bytes: written}, nil
}

// Get downloads a file from the remote host described by the resource.
func (c *Connector) Get(ctx context.Context, res *resource.Resource, remotePath, localPath string) (*TransferResult, error) {
	backend, err := c.connect(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("ssh get: %w", err)
	}
	defer backend.Close() //nolint:errcheck

	written, err := backend.Get(ctx, remotePath, localPath)
	if err != nil {
		return nil, fmt.Errorf("ssh get: %w", err)
	}
	return &TransferResult{LocalPath: localPath, RemotePath: remotePath, Bytes: written}, nil
}

// HostStatusJSON returns the connectivity status of a remote host as JSON.
func (c *Connector) HostStatusJSON(ctx context.Context, res *resource.Resource) (string, error) {
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
func (c *Connector) ExecJSON(ctx context.Context, res *resource.Resource, command string) (string, error) {
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
func (c *Connector) connect(ctx context.Context, res *resource.Resource) (Backend, error) {
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

	backend := c.newBackend()
	hostKey := hostKeyConfigFromResourceConfig(res.Config)
	if err := backend.Connect(ctx, host, port, user, keyFile, hostKey); err != nil {
		return nil, err
	}
	return backend, nil
}

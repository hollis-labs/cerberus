package forge

import (
	"context"
	"fmt"
	"strconv"

	contract "github.com/chrispian/cerberus/pkg/connector"
	"github.com/chrispian/cerberus/pkg/resource"
	"github.com/chrispian/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

type Backend interface {
	ListServers(ctx context.Context) ([]Server, error)
	GetServer(ctx context.Context, serverID int) (*Server, error)
	ListSites(ctx context.Context, serverID int) ([]Site, error)
	GetDeploymentScript(ctx context.Context, serverID, siteID int) (string, error)
	UpdateDeploymentScript(ctx context.Context, serverID, siteID int, content string, autoSource bool) error
	DeploySite(ctx context.Context, serverID, siteID int) error
	ExecuteSiteCommand(ctx context.Context, serverID, siteID int, command string) (*SiteCommand, error)
}

// Connector is a read-only connector for Laravel Forge servers.
// It supports status checks only — all write operations return an error.
// This is a transitional connector that will be removed once migration
// from Forge is complete.
type Connector struct {
	backend Backend
}

// New creates a Forge connector using a token from the secret provider.
func New(secrets secret.Provider) (*Connector, error) {
	if secrets == nil {
		return nil, fmt.Errorf("forge: secret provider is not configured")
	}
	token, err := secrets.Get(context.Background(), "forge", "api_token")
	if err != nil {
		return nil, fmt.Errorf("forge: get token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("forge: no API token — set CERBERUS_FORGE_API_TOKEN or configure its secret reference in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md)")
	}

	return &Connector{backend: NewClient(token)}, nil
}

// NewWithClient creates a connector with an explicit client (for testing).
func NewWithClient(client *Client) *Connector {
	return &Connector{backend: client}
}

// NewWithBackend creates a connector with an explicit backend (for testing).
func NewWithBackend(backend Backend) *Connector {
	return &Connector{backend: backend}
}

func (c *Connector) ID() string              { return "forge" }
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
		ID:            "forge",
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
				{Name: "server_id", Type: "integer", Description: "Forge server ID."},
				{Name: "site_id", Type: "integer", Description: "Forge site ID."},
				{Name: "command", Type: "string", Description: "Command to execute within the site's root directory."},
				{Name: "content", Type: "string", Description: "Deployment script content."},
				{Name: "auto_source", Type: "boolean", Description: "Automatically source environment variables in the deploy script."},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        "api_token",
				Description: "Laravel Forge API token.",
				Env:         "CERBERUS_FORGE_API_TOKEN",
				Required:    true,
			}},
		},
		Operations: []contract.Operation{
			{Name: "list_servers", Description: "List Forge servers.", InputSchema: contract.ObjectSchema(map[string]any{})},
			{Name: "get_server", Description: "Get a Forge server.", InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID.")}, "server_id")},
			{Name: "list_sites", Description: "List sites on a Forge server.", InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID.")}, "server_id")},
			{Name: "get_deployment_script", Description: "Read a site's deployment script.", InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID."), "site_id": contract.IntegerSchema("Forge site ID.")}, "server_id", "site_id")},
			{Name: "update_deployment_script", Description: "Update a site's deployment script.", InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID."), "site_id": contract.IntegerSchema("Forge site ID."), "content": contract.StringSchema("Deployment script content."), "auto_source": map[string]any{"type": "boolean", "description": "Automatically source environment variables."}}, "server_id", "site_id", "content")},
			{Name: "deploy_site", Description: "Trigger a Forge site deployment.", Examples: []string{"cerberus forge deploy 12 34 --dry-run", "cerberus forge deploy 12 34 --ack"}, InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID."), "site_id": contract.IntegerSchema("Forge site ID.")}, "server_id", "site_id"), Destructive: true, SupportsDry: true},
			{Name: "exec_site_command", Description: "Execute a command on a Forge site.", Examples: []string{"cerberus forge exec 12 34 'php artisan migrate --force' --dry-run", "cerberus forge exec 12 34 'php artisan migrate --force' --ack"}, InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID."), "site_id": contract.IntegerSchema("Forge site ID."), "command": contract.StringSchema("Command to execute.")}, "server_id", "site_id", "command"), Destructive: true, SupportsDry: true},
		},
	}
}

func (c *Connector) Definition() contract.Definition { return Definition() }

// Status checks the current state of a Forge server.
func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	id, err := serverID(res)
	if err != nil {
		return resource.StateUnknown, err
	}

	server, err := c.backend.GetServer(ctx, id)
	if err != nil {
		return resource.StateUnknown, fmt.Errorf("forge status: %w", err)
	}

	if server.IsReady {
		return resource.StateRunning, nil
	}
	return resource.StateUnhealthy, nil
}

// Create is not supported — Forge connector is read-only.
func (c *Connector) Create(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Start is not supported — Forge connector is read-only.
func (c *Connector) Start(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Stop is not supported — Forge connector is read-only.
func (c *Connector) Stop(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Destroy is not supported — Forge connector is read-only.
func (c *Connector) Destroy(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// ListServers returns all Forge servers on the account.
func (c *Connector) ListServers(ctx context.Context) ([]Server, error) {
	return c.backend.ListServers(ctx)
}

// GetServer returns a single Forge server by ID.
func (c *Connector) GetServer(ctx context.Context, serverID int) (*Server, error) {
	return c.backend.GetServer(ctx, serverID)
}

// ListSites returns all sites on a Forge server.
func (c *Connector) ListSites(ctx context.Context, serverID int) ([]Site, error) {
	return c.backend.ListSites(ctx, serverID)
}

func (c *Connector) GetDeploymentScript(ctx context.Context, serverID, siteID int) (string, error) {
	return c.backend.GetDeploymentScript(ctx, serverID, siteID)
}

func (c *Connector) UpdateDeploymentScript(ctx context.Context, serverID, siteID int, content string, autoSource bool) error {
	return c.backend.UpdateDeploymentScript(ctx, serverID, siteID, content, autoSource)
}

func (c *Connector) DeploySite(ctx context.Context, serverID, siteID int) error {
	return c.backend.DeploySite(ctx, serverID, siteID)
}

func (c *Connector) ExecuteSiteCommand(ctx context.Context, serverID, siteID int, command string) (*SiteCommand, error) {
	return c.backend.ExecuteSiteCommand(ctx, serverID, siteID, command)
}

func serverID(res *resource.Resource) (int, error) {
	switch v := res.Config["server_id"].(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case int64:
		return int(v), nil
	case string:
		id, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("forge: invalid server_id %q: %w", v, err)
		}
		return id, nil
	default:
		return 0, fmt.Errorf("forge: server_id not set in resource config")
	}
}

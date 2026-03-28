package forge

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector is a read-only connector for Laravel Forge servers.
// It supports status checks only — all write operations return an error.
// This is a transitional connector that will be removed once migration
// from Forge is complete.
type Connector struct {
	client *Client
}

// New creates a Forge connector using a token from the secret provider.
func New(secrets domain.SecretProvider) (*Connector, error) {
	token, err := secrets.Get(context.Background(), "forge", "api_token")
	if err != nil {
		return nil, fmt.Errorf("forge: get token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("forge: no API token — set CERBERUS_FORGE_API_TOKEN or store via cerberus secrets set forge api_token")
	}

	return &Connector{client: NewClient(token)}, nil
}

// NewWithClient creates a connector with an explicit client (for testing).
func NewWithClient(client *Client) *Connector {
	return &Connector{client: client}
}

func (c *Connector) ID() string              { return "forge" }
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

// Status checks the current state of a Forge server.
func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	id, err := serverID(res)
	if err != nil {
		return domain.StateUnknown, err
	}

	server, err := c.client.GetServer(ctx, id)
	if err != nil {
		return domain.StateUnknown, fmt.Errorf("forge status: %w", err)
	}

	if server.IsReady {
		return domain.StateRunning, nil
	}
	return domain.StateUnhealthy, nil
}

// Create is not supported — Forge connector is read-only.
func (c *Connector) Create(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Start is not supported — Forge connector is read-only.
func (c *Connector) Start(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Stop is not supported — Forge connector is read-only.
func (c *Connector) Stop(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// Destroy is not supported — Forge connector is read-only.
func (c *Connector) Destroy(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("not supported: forge connector is read-only")
}

// ListServers returns all Forge servers on the account.
func (c *Connector) ListServers(ctx context.Context) ([]Server, error) {
	return c.client.ListServers(ctx)
}

// GetServer returns a single Forge server by ID.
func (c *Connector) GetServer(ctx context.Context, serverID int) (*Server, error) {
	return c.client.GetServer(ctx, serverID)
}

// ListSites returns all sites on a Forge server.
func (c *Connector) ListSites(ctx context.Context, serverID int) ([]Site, error) {
	return c.client.ListSites(ctx, serverID)
}

func serverID(res *domain.Resource) (int, error) {
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

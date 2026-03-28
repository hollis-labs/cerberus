package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/digitalocean/godo"
)

// Connector manages DigitalOcean droplets via the godo SDK.
type Connector struct {
	client *godo.Client
}

// New creates a DigitalOcean connector using a token from the secret provider.
func New(secrets domain.SecretProvider) (*Connector, error) {
	token, err := secrets.Get(context.Background(), "digitalocean", "api_token")
	if err != nil {
		return nil, fmt.Errorf("digitalocean: get token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("digitalocean: no API token — set CERBERUS_DIGITALOCEAN_API_TOKEN or store via cerberus secrets set digitalocean api_token")
	}

	client := godo.NewFromToken(token)
	return &Connector{client: client}, nil
}

// NewWithClient creates a connector with an explicit godo client (for testing).
func NewWithClient(client *godo.Client) *Connector {
	return &Connector{client: client}
}

func (c *Connector) ID() string              { return "digitalocean" }
func (c *Connector) ResourceTypes() []string { return []string{"server"} }

func (c *Connector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{
		CanCreate:  true,
		CanDestroy: true,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

// Create provisions a new droplet from the resource config.
func (c *Connector) Create(ctx context.Context, res *domain.Resource) error {
	cfg := res.Config

	name, _ := cfg["name"].(string)
	if name == "" {
		name = res.Name
	}
	region, _ := cfg["region"].(string)
	if region == "" {
		return fmt.Errorf("digitalocean create: region is required in config")
	}
	size, _ := cfg["size"].(string)
	if size == "" {
		return fmt.Errorf("digitalocean create: size is required in config")
	}
	image, _ := cfg["image"].(string)
	if image == "" {
		return fmt.Errorf("digitalocean create: image is required in config")
	}

	createReq := &godo.DropletCreateRequest{
		Name:   name,
		Region: region,
		Size:   size,
		Image: godo.DropletCreateImage{
			Slug: image,
		},
	}

	// Optional SSH keys
	if keys, ok := cfg["ssh_keys"].([]any); ok {
		for _, k := range keys {
			if s, ok := k.(string); ok {
				createReq.SSHKeys = append(createReq.SSHKeys, godo.DropletCreateSSHKey{Fingerprint: s})
			}
		}
	}

	// Optional user data
	if ud, ok := cfg["user_data"].(string); ok {
		createReq.UserData = ud
	}

	droplet, _, err := c.client.Droplets.Create(ctx, createReq)
	if err != nil {
		return fmt.Errorf("digitalocean create: %w", err)
	}

	// Store droplet ID back in resource config for future operations
	res.Config["droplet_id"] = droplet.ID

	return nil
}

// Start powers on a droplet.
func (c *Connector) Start(ctx context.Context, res *domain.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	_, _, err = c.client.DropletActions.PowerOn(ctx, id)
	if err != nil {
		return fmt.Errorf("digitalocean start: %w", err)
	}
	return nil
}

// Stop powers off a droplet.
func (c *Connector) Stop(ctx context.Context, res *domain.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	_, _, err = c.client.DropletActions.PowerOff(ctx, id)
	if err != nil {
		return fmt.Errorf("digitalocean stop: %w", err)
	}
	return nil
}

// Destroy deletes a droplet permanently.
func (c *Connector) Destroy(ctx context.Context, res *domain.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	_, err = c.client.Droplets.Delete(ctx, id)
	if err != nil {
		return fmt.Errorf("digitalocean destroy: %w", err)
	}
	return nil
}

// Status checks the current state of a droplet.
func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	id, err := dropletID(res)
	if err != nil {
		return domain.StateUnknown, err
	}

	droplet, _, err := c.client.Droplets.Get(ctx, id)
	if err != nil {
		return domain.StateUnknown, fmt.Errorf("digitalocean status: %w", err)
	}

	switch droplet.Status {
	case "active":
		return domain.StateRunning, nil
	case "off":
		return domain.StateStopped, nil
	case "new":
		return domain.StateStarting, nil
	case "archive":
		return domain.StateDestroyed, nil
	default:
		return domain.StateUnknown, nil
	}
}

// GetDroplet returns the full droplet status as a normalized struct.
func (c *Connector) GetDroplet(ctx context.Context, id int) (*DropletStatus, error) {
	droplet, _, err := c.client.Droplets.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("digitalocean get: %w", err)
	}
	return mapDroplet(droplet), nil
}

// ListDroplets returns all droplets.
func (c *Connector) ListDroplets(ctx context.Context) ([]DropletStatus, error) {
	opt := &godo.ListOptions{PerPage: 100}
	droplets, _, err := c.client.Droplets.List(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("digitalocean list: %w", err)
	}

	result := make([]DropletStatus, len(droplets))
	for i, d := range droplets {
		result[i] = *mapDroplet(&d)
	}
	return result, nil
}

// StatusJSON returns droplet status as JSON (used by MCP tools).
func (c *Connector) StatusJSON(ctx context.Context, res *domain.Resource) (string, error) {
	id, err := dropletID(res)
	if err != nil {
		return "", err
	}
	status, err := c.GetDroplet(ctx, id)
	if err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(status, "", "  ")
	return string(data), nil
}

func mapDroplet(d *godo.Droplet) *DropletStatus {
	created, _ := time.Parse(time.RFC3339, d.Created)
	ds := &DropletStatus{
		ID:        d.ID,
		Name:      d.Name,
		Status:    d.Status,
		Region:    d.Region.Slug,
		Size:      d.Size.Slug,
		CreatedAt: created.UTC(),
	}
	if d.Image != nil {
		ds.Image = d.Image.Slug
	}
	// Extract public IPv4
	for _, net := range d.Networks.V4 {
		if net.Type == "public" {
			ds.IPv4 = net.IPAddress
			break
		}
	}
	return ds
}

func dropletID(res *domain.Resource) (int, error) {
	switch v := res.Config["droplet_id"].(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case int64:
		return int(v), nil
	case string:
		id, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("digitalocean: invalid droplet_id %q: %w", v, err)
		}
		return id, nil
	default:
		return 0, fmt.Errorf("digitalocean: droplet_id not set — run Create first or set droplet_id in config")
	}
}

package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

type Backend interface {
	CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error)
	PowerOnDroplet(ctx context.Context, id int) error
	PowerOffDroplet(ctx context.Context, id int) error
	DeleteDroplet(ctx context.Context, id int) error
	GetDroplet(ctx context.Context, id int) (*godo.Droplet, error)
	ListDroplets(ctx context.Context) ([]godo.Droplet, error)
}

type apiBackend struct {
	client *godo.Client
}

func (b *apiBackend) CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	droplet, _, err := b.client.Droplets.Create(ctx, req)
	return droplet, err
}

func (b *apiBackend) PowerOnDroplet(ctx context.Context, id int) error {
	_, _, err := b.client.DropletActions.PowerOn(ctx, id)
	return err
}

func (b *apiBackend) PowerOffDroplet(ctx context.Context, id int) error {
	_, _, err := b.client.DropletActions.PowerOff(ctx, id)
	return err
}

func (b *apiBackend) DeleteDroplet(ctx context.Context, id int) error {
	_, err := b.client.Droplets.Delete(ctx, id)
	return err
}

func (b *apiBackend) GetDroplet(ctx context.Context, id int) (*godo.Droplet, error) {
	droplet, _, err := b.client.Droplets.Get(ctx, id)
	return droplet, err
}

func (b *apiBackend) ListDroplets(ctx context.Context) ([]godo.Droplet, error) {
	opt := &godo.ListOptions{PerPage: 100}
	droplets, _, err := b.client.Droplets.List(ctx, opt)
	return droplets, err
}

// Connector manages DigitalOcean droplets via the godo SDK.
type Connector struct {
	backend Backend
}

// New creates a DigitalOcean connector using a token from the secret provider.
func New(secrets secret.Provider) (*Connector, error) {
	if secrets == nil {
		return nil, fmt.Errorf("digitalocean: secret provider is not configured")
	}
	token, err := secrets.Get(context.Background(), "digitalocean", "api_token")
	if err != nil {
		return nil, fmt.Errorf("digitalocean: get token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("digitalocean: no API token — set CERBERUS_DIGITALOCEAN_API_TOKEN or configure its secret reference in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md)")
	}

	client := godo.NewFromToken(token)
	return &Connector{backend: &apiBackend{client: client}}, nil
}

// NewWithClient creates a connector with an explicit godo client (for testing).
func NewWithClient(client *godo.Client) *Connector {
	return &Connector{backend: &apiBackend{client: client}}
}

// NewWithBackend creates a connector with an explicit backend (for testing).
func NewWithBackend(backend Backend) *Connector {
	return &Connector{backend: backend}
}

func (c *Connector) ID() string              { return "digitalocean" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Server)} }

func (c *Connector) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		CanCreate:  true,
		CanDestroy: true,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  true,
	}
}

func Definition() contract.Definition {
	return contract.Definition{
		ID:            "digitalocean",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanCreate:  true,
			CanDestroy: true,
			CanBuild:   false,
			CanLogs:    false,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "droplet_id", Type: "integer", Description: "DigitalOcean droplet ID."},
				{Name: "name", Type: "string", Description: "Droplet name."},
				{Name: "region", Type: "string", Description: "Droplet region slug."},
				{Name: "size", Type: "string", Description: "Droplet size slug."},
				{Name: "image", Type: "string", Description: "Droplet image slug.", Required: true},
				{Name: "ssh_keys", Type: "array", Description: "SSH key fingerprints."},
				{Name: "user_data", Type: "string", Description: "Cloud-init user-data."},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        "api_token",
				Description: "DigitalOcean API token.",
				Env:         "CERBERUS_DIGITALOCEAN_API_TOKEN",
				Required:    true,
			}},
		},
		Operations: []contract.Operation{
			{
				Name:        "list_droplets",
				Description: "List DigitalOcean droplets.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:        "get_droplet",
				Description: "Get detailed status for a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
			},
			{
				Name:        "create_droplet",
				Description: "Create a new DigitalOcean droplet.",
				Examples: []string{
					"cerberus server create --name web-1 --region nyc3 --size s-1vcpu-1gb --image ubuntu-24-04-x64 --dry-run",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"name":      contract.StringSchema("Droplet name."),
					"region":    contract.StringSchema("Droplet region slug."),
					"size":      contract.StringSchema("Droplet size slug."),
					"image":     contract.StringSchema("Droplet image slug."),
					"ssh_keys":  map[string]any{"type": "array", "description": "SSH key fingerprints.", "items": map[string]any{"type": "string"}},
					"user_data": contract.StringSchema("Cloud-init user-data."),
				}, "name", "region", "size", "image"),
				SupportsDry: true,
			},
			{
				Name:        "start",
				Description: "Power on a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
			},
			{
				Name:        "stop",
				Description: "Power off a droplet.",
				Examples: []string{
					"cerberus server stop 123456 --dry-run",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
				SupportsDry: true,
			},
			{
				Name:        "destroy",
				Description: "Destroy a droplet permanently.",
				Examples: []string{
					"cerberus server destroy 123456 --dry-run",
					"cerberus server destroy 123456 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
				Destructive: true,
				SupportsDry: true,
			},
			{
				Name:        "status",
				Description: "Read normalized runtime state for a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
			},
		},
	}
}

func (c *Connector) Definition() contract.Definition {
	return Definition()
}

// Create provisions a new droplet from the resource config.
func (c *Connector) Create(ctx context.Context, res *resource.Resource) error {
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
		Image:  godo.DropletCreateImage{Slug: image},
	}

	if keys, ok := cfg["ssh_keys"].([]any); ok {
		for _, k := range keys {
			if s, ok := k.(string); ok {
				createReq.SSHKeys = append(createReq.SSHKeys, godo.DropletCreateSSHKey{Fingerprint: s})
			}
		}
	}
	if keys, ok := cfg["ssh_keys"].([]string); ok {
		for _, s := range keys {
			createReq.SSHKeys = append(createReq.SSHKeys, godo.DropletCreateSSHKey{Fingerprint: s})
		}
	}
	if ud, ok := cfg["user_data"].(string); ok {
		createReq.UserData = ud
	}

	droplet, err := c.backend.CreateDroplet(ctx, createReq)
	if err != nil {
		return fmt.Errorf("digitalocean create: %w", err)
	}
	if droplet != nil {
		res.Config["droplet_id"] = droplet.ID
	}
	return nil
}

func (c *Connector) Start(ctx context.Context, res *resource.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	if err := c.backend.PowerOnDroplet(ctx, id); err != nil {
		return fmt.Errorf("digitalocean start: %w", err)
	}
	return nil
}

func (c *Connector) Stop(ctx context.Context, res *resource.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	if err := c.backend.PowerOffDroplet(ctx, id); err != nil {
		return fmt.Errorf("digitalocean stop: %w", err)
	}
	return nil
}

func (c *Connector) Destroy(ctx context.Context, res *resource.Resource) error {
	id, err := dropletID(res)
	if err != nil {
		return err
	}
	if err := c.backend.DeleteDroplet(ctx, id); err != nil {
		return fmt.Errorf("digitalocean destroy: %w", err)
	}
	return nil
}

func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	id, err := dropletID(res)
	if err != nil {
		return resource.StateUnknown, err
	}

	droplet, err := c.backend.GetDroplet(ctx, id)
	if err != nil {
		return resource.StateUnknown, fmt.Errorf("digitalocean status: %w", err)
	}
	return dropletState(droplet), nil
}

func (c *Connector) GetDroplet(ctx context.Context, id int) (*DropletStatus, error) {
	droplet, err := c.backend.GetDroplet(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("digitalocean get: %w", err)
	}
	return mapDroplet(droplet), nil
}

func (c *Connector) ListDroplets(ctx context.Context) ([]DropletStatus, error) {
	droplets, err := c.backend.ListDroplets(ctx)
	if err != nil {
		return nil, fmt.Errorf("digitalocean list: %w", err)
	}
	result := make([]DropletStatus, len(droplets))
	for i, d := range droplets {
		result[i] = *mapDroplet(&d)
	}
	return result, nil
}

func (c *Connector) StatusJSON(ctx context.Context, res *resource.Resource) (string, error) {
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
		CreatedAt: created.UTC(),
	}
	if d.Region != nil {
		ds.Region = d.Region.Slug
	}
	if d.Size != nil {
		ds.Size = d.Size.Slug
	}
	if d.Image != nil {
		ds.Image = d.Image.Slug
	}
	if d.Networks != nil {
		for _, net := range d.Networks.V4 {
			if net.Type == "public" {
				ds.IPv4 = net.IPAddress
				break
			}
		}
	}
	return ds
}

func dropletID(res *resource.Resource) (int, error) {
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

func dropletState(droplet *godo.Droplet) resource.State {
	if droplet == nil {
		return resource.StateUnknown
	}
	switch droplet.Status {
	case "active":
		return resource.StateRunning
	case "off":
		return resource.StateStopped
	case "new":
		return resource.StateStarting
	case "archive":
		return resource.StateDestroyed
	default:
		return resource.StateUnknown
	}
}

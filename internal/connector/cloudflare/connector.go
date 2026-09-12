package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"

	contract "github.com/chrispian/cerberus/pkg/connector"
	"github.com/chrispian/cerberus/pkg/resource"
	"github.com/chrispian/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

// Connector manages Cloudflare resources (DNS records, zones) via either
// the cloudflare-go API SDK or the wrangler CLI, selected automatically.
type Connector struct {
	backend Backend
}

// New creates a Cloudflare connector. It tries the API backend first (if a token
// is available via secrets), then falls back to the wrangler CLI.
func New(secrets secret.Provider) (*Connector, error) {
	// Try API backend first
	if secrets != nil {
		token, err := secrets.Get(context.Background(), "cloudflare", "api_token")
		if err != nil {
			return nil, fmt.Errorf("cloudflare: resolve credential: %w", err)
		}
		if token != "" {
			b, err := NewAPIBackend(token)
			if err != nil {
				return nil, fmt.Errorf("cloudflare api backend: %w", err)
			}
			return &Connector{backend: b}, nil
		}
	}

	// Fall back to CLI
	if path, ok := DetectWrangler(); ok {
		return &Connector{backend: newCLIBackendWithPath(path)}, nil
	}

	return nil, fmt.Errorf("cloudflare connector: no API token and wrangler CLI not found — set CERBERUS_CLOUDFLARE_API_TOKEN or install wrangler")
}

// NewWithBackend creates a Cloudflare connector with an explicit backend.
func NewWithBackend(b Backend) *Connector {
	return &Connector{backend: b}
}

func (c *Connector) ID() string              { return "cloudflare" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Domain)} }

func (c *Connector) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		CanCreate:  true,
		CanDestroy: true,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  false,
	}
}

func Definition() contract.Definition {
	return contract.Definition{
		ID:            "cloudflare",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Domain)},
		Capabilities: contract.Capabilities{
			CanCreate:  true,
			CanDestroy: true,
			CanBuild:   false,
			CanLogs:    false,
			CanHealth:  false,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        "zone_id",
					Type:        "string",
					Description: "Cloudflare zone ID.",
				},
				{
					Name:        "account_id",
					Type:        "string",
					Description: "Cloudflare account ID used for zone creation.",
				},
			},
			Secrets: []contract.SecretRequirement{
				{
					Name:        "api_token",
					Description: "Cloudflare API token used when the API backend is available.",
					Env:         "CERBERUS_CLOUDFLARE_API_TOKEN",
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:        "list_zones",
				Description: "List Cloudflare zones.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:        "create_zone",
				Description: "Create a Cloudflare zone in an account.",
				Examples: []string{
					"cerberus cloudflare zones create <account-id> chrispian.dev --type full --dry-run",
					"cerberus cloudflare zones create <account-id> chrispian.dev --type full --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id": contract.StringSchema("Cloudflare account ID."),
					"name":       contract.StringSchema("Zone name such as example.com."),
					"type":       contract.StringSchema("Zone type: full or partial."),
				}, "account_id", "name"),
				Destructive: true,
				SupportsDry: true,
			},
			{
				Name:        "list_dns_records",
				Description: "List DNS records for a Cloudflare zone.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id": contract.StringSchema("Cloudflare zone ID."),
				}, "zone_id"),
			},
			{
				Name:        "create_dns_record",
				Description: "Create a DNS record in a Cloudflare zone.",
				Examples: []string{
					"cerberus cloudflare dns create <zone-id> --type A --name app --content 203.0.113.10 --ttl 300 --dry-run",
					"cerberus cloudflare dns create <zone-id> --type CNAME --name www --content app.example.com --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id":  contract.StringSchema("Cloudflare zone ID."),
					"type":     contract.StringSchema("DNS record type."),
					"name":     contract.StringSchema("DNS record name."),
					"content":  contract.StringSchema("DNS record content."),
					"ttl":      contract.IntegerSchema("TTL in seconds."),
					"proxied":  map[string]any{"type": "boolean", "description": "Whether to proxy the record through Cloudflare."},
					"priority": contract.IntegerSchema("Priority for MX records."),
				}, "zone_id", "type", "name", "content"),
				Destructive: true,
				SupportsDry: true,
			},
			{
				Name:        "delete_dns_record",
				Description: "Delete a DNS record from a Cloudflare zone.",
				Examples: []string{
					"cerberus cloudflare dns delete <zone-id> <record-id> --dry-run",
					"cerberus cloudflare dns delete <zone-id> <record-id> --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id":   contract.StringSchema("Cloudflare zone ID."),
					"record_id": contract.StringSchema("Cloudflare DNS record ID."),
				}, "zone_id", "record_id"),
				Destructive: true,
				SupportsDry: true,
			},
		},
	}
}

func (c *Connector) Definition() contract.Definition {
	return Definition()
}

func (c *Connector) Start(_ context.Context, _ *resource.Resource) error {
	return nil
}

func (c *Connector) Stop(_ context.Context, _ *resource.Resource) error {
	return nil
}

// Status checks if the zone referenced by the resource exists and is active.
func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	zoneID, _ := res.Config["zone_id"].(string)
	if zoneID == "" {
		return resource.StateUnknown, fmt.Errorf("cloudflare resource %q missing zone_id in config", res.ID)
	}

	zones, err := c.backend.ListZones(ctx)
	if err != nil {
		return resource.StateUnknown, fmt.Errorf("cloudflare status: %w", err)
	}

	for _, z := range zones {
		if z.ID == zoneID {
			if z.Status == "active" {
				return resource.StateRunning, nil
			}
			return resource.StateStopped, nil
		}
	}

	return resource.StateUnknown, fmt.Errorf("cloudflare zone %s not found", zoneID)
}

// Create creates a DNS record from the resource config.
func (c *Connector) Create(ctx context.Context, res *resource.Resource) error {
	zoneID, _ := res.Config["zone_id"].(string)
	if zoneID == "" {
		return fmt.Errorf("cloudflare resource %q missing zone_id in config", res.ID)
	}

	rec := DNSRecord{
		Type:    stringFromConfig(res.Config, "record_type"),
		Name:    stringFromConfig(res.Config, "name"),
		Content: stringFromConfig(res.Config, "content"),
	}

	if ttl, ok := res.Config["ttl"].(float64); ok {
		rec.TTL = int(ttl)
	}
	if proxied, ok := res.Config["proxied"].(bool); ok {
		rec.Proxied = proxied
	}

	_, err := c.backend.CreateDNSRecord(ctx, zoneID, rec)
	if err != nil {
		return fmt.Errorf("cloudflare create: %w", err)
	}
	return nil
}

// Destroy deletes a DNS record from the resource config.
func (c *Connector) Destroy(ctx context.Context, res *resource.Resource) error {
	zoneID, _ := res.Config["zone_id"].(string)
	recordID, _ := res.Config["record_id"].(string)
	if zoneID == "" || recordID == "" {
		return fmt.Errorf("cloudflare resource %q missing zone_id or record_id in config", res.ID)
	}

	if err := c.backend.DeleteDNSRecord(ctx, zoneID, recordID); err != nil {
		return fmt.Errorf("cloudflare destroy: %w", err)
	}
	return nil
}

// --- Extra methods exposed for CLI/MCP ---

// ListZones returns all zones.
func (c *Connector) ListZones(ctx context.Context) ([]Zone, error) {
	return c.backend.ListZones(ctx)
}

// CreateZone creates a zone in the given account.
func (c *Connector) CreateZone(ctx context.Context, accountID, name, zoneType string) (*Zone, error) {
	if zoneType == "" {
		zoneType = ZoneTypeFull
	}
	return c.backend.CreateZone(ctx, accountID, name, zoneType)
}

// ListDNSRecords returns DNS records for a zone.
func (c *Connector) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	return c.backend.ListDNSRecords(ctx, zoneID)
}

// CreateDNSRecord creates a DNS record in a zone.
func (c *Connector) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error) {
	return c.backend.CreateDNSRecord(ctx, zoneID, rec)
}

// DeleteDNSRecord deletes a DNS record from a zone.
func (c *Connector) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	return c.backend.DeleteDNSRecord(ctx, zoneID, recordID)
}

// ZonesJSON returns zones as a JSON string (used by MCP tools).
func (c *Connector) ZonesJSON(ctx context.Context) (string, error) {
	zones, err := c.backend.ListZones(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(zones, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DNSRecordsJSON returns DNS records as a JSON string (used by MCP tools).
func (c *Connector) DNSRecordsJSON(ctx context.Context, zoneID string) (string, error) {
	records, err := c.backend.ListDNSRecords(ctx, zoneID)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// stringFromConfig extracts a string value from a config map.
func stringFromConfig(cfg map[string]any, key string) string {
	v, _ := cfg[key].(string)
	return v
}

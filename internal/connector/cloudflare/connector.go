package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector manages Cloudflare resources (DNS records, zones) via either
// the cloudflare-go API SDK or the wrangler CLI, selected automatically.
type Connector struct {
	backend Backend
}

// New creates a Cloudflare connector. It tries the API backend first (if a token
// is available via secrets), then falls back to the wrangler CLI.
func New(secrets domain.SecretProvider) (*Connector, error) {
	// Try API backend first
	if secrets != nil {
		token, _ := secrets.Get(context.Background(), "cloudflare", "api_token")
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
func (c *Connector) ResourceTypes() []string { return []string{"domain"} }

func (c *Connector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{
		CanCreate:  true,
		CanDestroy: true,
		CanBuild:   false,
		CanLogs:    false,
		CanHealth:  false,
	}
}

func (c *Connector) Start(_ context.Context, _ *domain.Resource) error {
	return nil
}

func (c *Connector) Stop(_ context.Context, _ *domain.Resource) error {
	return nil
}

// Status checks if the zone referenced by the resource exists and is active.
func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	zoneID, _ := res.Config["zone_id"].(string)
	if zoneID == "" {
		return domain.StateUnknown, fmt.Errorf("cloudflare resource %q missing zone_id in config", res.ID)
	}

	zones, err := c.backend.ListZones(ctx)
	if err != nil {
		return domain.StateUnknown, fmt.Errorf("cloudflare status: %w", err)
	}

	for _, z := range zones {
		if z.ID == zoneID {
			if z.Status == "active" {
				return domain.StateRunning, nil
			}
			return domain.StateStopped, nil
		}
	}

	return domain.StateUnknown, fmt.Errorf("cloudflare zone %s not found", zoneID)
}

// Create creates a DNS record from the resource config.
func (c *Connector) Create(ctx context.Context, res *domain.Resource) error {
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
func (c *Connector) Destroy(ctx context.Context, res *domain.Resource) error {
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

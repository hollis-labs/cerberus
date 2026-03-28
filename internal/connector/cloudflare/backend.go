package cloudflare

import "context"

// Backend defines how the Cloudflare connector communicates with Cloudflare.
// Two implementations: APIBackend (cloudflare-go SDK) and CLIBackend (wrangler CLI).
type Backend interface {
	// ListZones returns all zones in the account.
	ListZones(ctx context.Context) ([]Zone, error)

	// ListDNSRecords returns DNS records for a zone.
	ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)

	// CreateDNSRecord creates a new DNS record in a zone.
	CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error)

	// DeleteDNSRecord deletes a DNS record from a zone.
	DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error

	// ListTunnels returns tunnels for an account.
	ListTunnels(ctx context.Context, accountID string) ([]Tunnel, error)
}

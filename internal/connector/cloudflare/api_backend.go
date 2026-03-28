package cloudflare

import (
	"context"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/zero_trust"
	"github.com/cloudflare/cloudflare-go/v4/zones"
)

// APIBackend implements Backend using the cloudflare-go SDK.
type APIBackend struct {
	client *cf.Client
}

// NewAPIBackend creates an APIBackend authenticated with the given API token.
func NewAPIBackend(apiToken string) (*APIBackend, error) {
	client := cf.NewClient(option.WithAPIToken(apiToken))
	return &APIBackend{client: client}, nil
}

func (a *APIBackend) ListZones(ctx context.Context) ([]Zone, error) {
	pager := a.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{})

	var out []Zone
	for pager.Next() {
		z := pager.Current()
		out = append(out, Zone{
			ID:          z.ID,
			Name:        z.Name,
			Status:      string(z.Status),
			Paused:      z.Paused,
			NameServers: z.NameServers,
			Plan:        z.Plan.Name, //nolint:staticcheck // Plan field works, deprecation is SDK-internal
		})
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}

	return out, nil
}

func (a *APIBackend) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	pager := a.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
	})

	var out []DNSRecord
	for pager.Next() {
		r := pager.Current()
		rec := DNSRecord{
			ID:      r.ID,
			Type:    string(r.Type),
			Name:    r.Name,
			Content: r.Content,
			TTL:     int(r.TTL),
			Proxied: r.Proxied,
		}
		if r.Priority != 0 {
			p := int(r.Priority)
			rec.Priority = &p
		}
		out = append(out, rec)
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list dns records for zone %s: %w", zoneID, err)
	}

	return out, nil
}

func (a *APIBackend) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error) {
	body := dns.RecordNewParamsBody{
		Name:    cf.F(rec.Name),
		Type:    cf.F(dns.RecordNewParamsBodyType(rec.Type)),
		Content: cf.F(rec.Content),
		TTL:     cf.F(dns.TTL(rec.TTL)),
		Proxied: cf.F(rec.Proxied),
	}
	if rec.Priority != nil {
		body.Priority = cf.F(float64(*rec.Priority))
	}

	resp, err := a.client.DNS.Records.New(ctx, dns.RecordNewParams{
		ZoneID: cf.F(zoneID),
		Body:   body,
	})
	if err != nil {
		return nil, fmt.Errorf("create dns record in zone %s: %w", zoneID, err)
	}

	created := &DNSRecord{
		ID:      resp.ID,
		Type:    string(resp.Type),
		Name:    resp.Name,
		Content: resp.Content,
		TTL:     int(resp.TTL),
		Proxied: resp.Proxied,
	}
	if resp.Priority != 0 {
		p := int(resp.Priority)
		created.Priority = &p
	}
	return created, nil
}

func (a *APIBackend) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := a.client.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{
		ZoneID: cf.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete dns record %s in zone %s: %w", recordID, zoneID, err)
	}
	return nil
}

func (a *APIBackend) ListTunnels(ctx context.Context, accountID string) ([]Tunnel, error) {
	pager := a.client.ZeroTrust.Tunnels.ListAutoPaging(ctx, zero_trust.TunnelListParams{
		AccountID: cf.F(accountID),
	})

	var out []Tunnel
	for pager.Next() {
		t := pager.Current()
		out = append(out, Tunnel{
			ID:        t.ID,
			Name:      t.Name,
			Status:    string(t.Status),
			CreatedAt: t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list tunnels for account %s: %w", accountID, err)
	}

	return out, nil
}

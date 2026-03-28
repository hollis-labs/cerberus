package namecheap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector manages Namecheap domain resources via the Namecheap XML API.
type Connector struct {
	client *Client
}

// New creates a Namecheap connector using credentials from the secret provider.
func New(secrets domain.SecretProvider) (*Connector, error) {
	ctx := context.Background()

	apiUser, err := secrets.Get(ctx, "namecheap", "api_user")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get api_user: %w", err)
	}
	if apiUser == "" {
		return nil, fmt.Errorf("namecheap: no API user — set CERBERUS_NAMECHEAP_API_USER or store via cerberus secrets set namecheap api_user")
	}

	apiKey, err := secrets.Get(ctx, "namecheap", "api_key")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get api_key: %w", err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("namecheap: no API key — set CERBERUS_NAMECHEAP_API_KEY or store via cerberus secrets set namecheap api_key")
	}

	username, err := secrets.Get(ctx, "namecheap", "username")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get username: %w", err)
	}
	if username == "" {
		return nil, fmt.Errorf("namecheap: no username — set CERBERUS_NAMECHEAP_USERNAME or store via cerberus secrets set namecheap username")
	}

	clientIP := os.Getenv("CERBERUS_NAMECHEAP_CLIENT_IP")
	if clientIP == "" {
		clientIP = "127.0.0.1"
	}

	return &Connector{client: NewClient(apiUser, apiKey, username, clientIP)}, nil
}

// NewWithClient creates a connector with an explicit client (for testing).
func NewWithClient(client *Client) *Connector {
	return &Connector{client: client}
}

func (c *Connector) ID() string              { return "namecheap" }
func (c *Connector) ResourceTypes() []string { return []string{"domain"} }

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
	return fmt.Errorf("namecheap connector does not support Create")
}

func (c *Connector) Start(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("namecheap connector does not support Start")
}

func (c *Connector) Stop(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("namecheap connector does not support Stop")
}

func (c *Connector) Destroy(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("namecheap connector does not support Destroy")
}

// Status checks if the domain is registered and not expired.
func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	domainName, _ := res.Config["domain"].(string)
	if domainName == "" {
		return domain.StateUnknown, fmt.Errorf("namecheap resource %q missing domain in config", res.ID)
	}

	status, err := c.client.GetDomainStatus(ctx, domainName)
	if err != nil {
		return domain.StateUnknown, fmt.Errorf("namecheap status: %w", err)
	}

	if !status.Registered {
		return domain.StateStopped, nil
	}
	return domain.StateRunning, nil
}

// ListDomains returns all domains in the account.
func (c *Connector) ListDomains(ctx context.Context) ([]Domain, error) {
	return c.client.ListDomains(ctx)
}

// GetDomainStatus returns detailed status for a domain.
func (c *Connector) GetDomainStatus(ctx context.Context, domainName string) (*DomainStatus, error) {
	return c.client.GetDomainStatus(ctx, domainName)
}

// ListDNSRecords returns DNS records for a domain. The domain is split into SLD+TLD.
func (c *Connector) ListDNSRecords(ctx context.Context, domainName string) ([]DNSRecord, error) {
	sld, tld, err := SplitDomain(domainName)
	if err != nil {
		return nil, err
	}
	return c.client.ListDNSRecords(ctx, sld, tld)
}

// DomainsJSON returns the domain list as a JSON string (used by MCP tools).
func (c *Connector) DomainsJSON(ctx context.Context) (string, error) {
	domains, err := c.client.ListDomains(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(domains, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DomainStatusJSON returns domain status as a JSON string (used by MCP tools).
func (c *Connector) DomainStatusJSON(ctx context.Context, domainName string) (string, error) {
	status, err := c.client.GetDomainStatus(ctx, domainName)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DNSRecordsJSON returns DNS records as a JSON string (used by MCP tools).
func (c *Connector) DNSRecordsJSON(ctx context.Context, domainName string) (string, error) {
	records, err := c.ListDNSRecords(ctx, domainName)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SplitDomain splits a domain name into SLD and TLD at the first dot.
// Example: "example.com" -> ("example", "com")
func SplitDomain(domain string) (string, string, error) {
	parts := strings.SplitN(domain, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid domain %q: expected format sld.tld", domain)
	}
	return parts[0], parts[1], nil
}

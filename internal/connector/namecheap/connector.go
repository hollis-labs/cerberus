package namecheap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

var _ contract.Connector = (*Connector)(nil)
var _ contract.Describer = (*Connector)(nil)

// ErrUnsafePerRecordWrite protects records that getHosts silently omits.
var ErrUnsafePerRecordWrite = errors.New("namecheap per-record create/delete is disabled: getHosts can omit existing records and setHosts replaces the entire zone, risking silent data loss; use the Namecheap dashboard or explicit set_dns_record_set (MCP: cerberus_set_dns_record_set) with a complete authoritative record set and email_type")

type Backend interface {
	ListDomains(ctx context.Context) ([]Domain, error)
	GetDomainStatus(ctx context.Context, domain string) (*DomainStatus, error)
	GetDNSRecordSet(ctx context.Context, sld, tld string) (*DNSRecordSet, error)
	SetDNSRecordSet(ctx context.Context, sld, tld string, set DNSRecordSet) error
	SetCustomNameservers(ctx context.Context, domain string, nameservers []string) (*DomainNameserverUpdate, error)
}

// Connector manages Namecheap domain resources via the Namecheap XML API.
type Connector struct {
	backend Backend
}

// New creates a Namecheap connector using credentials from the secret provider.
func New(secrets secret.Provider) (*Connector, error) {
	ctx := context.Background()
	if secrets == nil {
		return nil, fmt.Errorf("namecheap: secret provider is not configured")
	}

	apiUser, err := secrets.Get(ctx, "namecheap", "api_user")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get api_user: %w", err)
	}
	if apiUser == "" {
		return nil, fmt.Errorf("namecheap: no API user — set CERBERUS_NAMECHEAP_API_USER or configure its secret reference in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md)")
	}

	apiKey, err := secrets.Get(ctx, "namecheap", "api_key")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get api_key: %w", err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("namecheap: no API key — set CERBERUS_NAMECHEAP_API_KEY or configure its secret reference in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md)")
	}

	username, err := secrets.Get(ctx, "namecheap", "username")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get username: %w", err)
	}
	if username == "" {
		return nil, fmt.Errorf("namecheap: no username — set CERBERUS_NAMECHEAP_USERNAME or configure its secret reference in ~/.cerberus/connector-secrets.yaml (see docs/secrets.md)")
	}

	clientIP, err := secrets.Get(ctx, "namecheap", "client_ip")
	if err != nil {
		return nil, fmt.Errorf("namecheap: get client_ip: %w", err)
	}
	if clientIP == "" {
		clientIP = "127.0.0.1"
	}

	return &Connector{backend: NewClient(apiUser, apiKey, username, clientIP)}, nil
}

// NewWithClient creates a connector with an explicit client (for testing).
func NewWithClient(client *Client) *Connector {
	return &Connector{backend: client}
}

// NewWithBackend creates a connector with an explicit backend (for testing).
func NewWithBackend(backend Backend) *Connector {
	return &Connector{backend: backend}
}

func (c *Connector) ID() string              { return "namecheap" }
func (c *Connector) ResourceTypes() []string { return []string{string(resource.Domain)} }

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
	return contract.Finalize(contract.Definition{
		ID:            "namecheap",
		Version:       "builtin",
		ResourceTypes: []string{string(resource.Domain)},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
			CanBuild:   false,
			CanLogs:    false,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        "domain",
					Type:        "string",
					Description: "Domain name in sld.tld form.",
				},
			},
			Secrets: []contract.SecretRequirement{
				{
					Name:        "api_user",
					Description: "Namecheap API user.",
					Env:         "CERBERUS_NAMECHEAP_API_USER",
					Required:    true,
				},
				{
					Name:        "api_key",
					Description: "Namecheap API key.",
					Env:         "CERBERUS_NAMECHEAP_API_KEY",
					Required:    true,
				},
				{
					Name:        "username",
					Description: "Namecheap username.",
					Env:         "CERBERUS_NAMECHEAP_USERNAME",
					Required:    true,
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name: "get_dns_record_set", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}}, Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone, Description: "Read visible host records and domain email_type. getHosts may omit records; this is not an authoritative zone backup.",
				InputSchema: contract.ObjectSchema(map[string]any{"domain": contract.StringSchema("Domain name.")}, "domain"),
			},
			{
				Name: "set_dns_record_set", Effect: contract.EffectWrite, Target: contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}}, Preview: contract.PreviewHost, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone, Description: "Replace ALL DNS hosts and explicitly set domain email routing. Supply an authoritative complete records array, including records getHosts hides. Omitted records are deleted.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain":     contract.StringSchema("Domain name."),
					"email_type": map[string]any{"type": "string", "enum": []string{"MX", "MXE", "FWD", "OX", "NONE"}},
					"records": map[string]any{"type": "array", "items": contract.ObjectSchema(map[string]any{
						"type": contract.StringSchema("DNS type."), "host": contract.StringSchema("Host name."), "value": contract.StringSchema("Record value."), "ttl": contract.IntegerSchema("TTL."), "mx_pref": contract.IntegerSchema("MX preference."),
					}, "type", "host", "value")},
				}, "domain", "email_type", "records"),
			},
			{
				Name:        "list_domains",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.account"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List domains in the Namecheap account.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:        "get_domain_status",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read detailed status for a Namecheap domain.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain": contract.StringSchema("Domain name in sld.tld form."),
				}, "domain"),
			},
			{
				Name:        "list_dns_records",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List DNS records for a Namecheap domain.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain": contract.StringSchema("Domain name in sld.tld form."),
				}, "domain"),
			},
			{
				Name:        "set_custom_nameservers",
				Effect:      contract.EffectWrite,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewHost,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Switch a Namecheap domain to a custom nameserver set.",
				Examples: []string{
					"cerberus domain nameservers set example.com ns1.example.net ns2.example.net --dry-run",
					"cerberus domain nameservers set example.com ns1.example.net ns2.example.net --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain": contract.StringSchema("Domain name in sld.tld form."),
					"nameservers": map[string]any{
						"type":        "array",
						"description": "List of nameservers to assign to the domain.",
						"items":       map[string]any{"type": "string"},
						"minItems":    2,
					},
				}, "domain", "nameservers"),
			},
		},
	})
}

func (c *Connector) Definition() contract.Definition {
	return Definition()
}

func (c *Connector) Create(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("namecheap connector does not support Create")
}

func (c *Connector) Start(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("namecheap connector does not support Start")
}

func (c *Connector) Stop(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("namecheap connector does not support Stop")
}

func (c *Connector) Destroy(_ context.Context, _ *resource.Resource) error {
	return fmt.Errorf("namecheap connector does not support Destroy")
}

// Status checks if the domain is registered and not expired.
func (c *Connector) Status(ctx context.Context, res *resource.Resource) (resource.State, error) {
	domainName, _ := res.Config["domain"].(string)
	if domainName == "" {
		return resource.StateUnknown, fmt.Errorf("namecheap resource %q missing domain in config", res.ID)
	}

	status, err := c.backend.GetDomainStatus(ctx, domainName)
	if err != nil {
		return resource.StateUnknown, fmt.Errorf("namecheap status: %w", err)
	}

	if !status.Registered {
		return resource.StateStopped, nil
	}
	return resource.StateRunning, nil
}

// ListDomains returns all domains in the account.
func (c *Connector) ListDomains(ctx context.Context) ([]Domain, error) {
	return c.backend.ListDomains(ctx)
}

// GetDomainStatus returns detailed status for a domain.
func (c *Connector) GetDomainStatus(ctx context.Context, domainName string) (*DomainStatus, error) {
	return c.backend.GetDomainStatus(ctx, domainName)
}

// ListDNSRecords returns DNS records for a domain. The domain is split into SLD+TLD.
func (c *Connector) ListDNSRecords(ctx context.Context, domainName string) ([]DNSRecord, error) {
	set, err := c.GetDNSRecordSet(ctx, domainName)
	if err != nil {
		return nil, err
	}
	return set.Records, nil
}

func (c *Connector) GetDNSRecordSet(ctx context.Context, domainName string) (*DNSRecordSet, error) {
	sld, tld, err := SplitDomain(domainName)
	if err != nil {
		return nil, err
	}
	return c.backend.GetDNSRecordSet(ctx, sld, tld)
}

// SetDNSRecords replaces caller-supplied hosts while preserving the current email mode.
func (c *Connector) SetDNSRecords(ctx context.Context, domainName string, records []DNSRecord) error {
	set, err := c.GetDNSRecordSet(ctx, domainName)
	if err != nil {
		return err
	}
	set.Records = records
	return c.SetDNSRecordSet(ctx, domainName, *set)
}

// SetDNSRecordSet explicitly replaces all hosts and the email mode. The caller
// must supply an authoritative complete set, not a getHosts reconstruction.
func (c *Connector) SetDNSRecordSet(ctx context.Context, domainName string, set DNSRecordSet) error {
	if err := set.Validate(); err != nil {
		return err
	}
	sld, tld, err := SplitDomain(domainName)
	if err != nil {
		return err
	}
	return c.backend.SetDNSRecordSet(ctx, sld, tld, set)
}

// CreateDNSRecord is retained for compatibility, but never reads or writes DNS.
func (c *Connector) CreateDNSRecord(_ context.Context, _ string, _ DNSRecord) (*DNSRecord, error) {
	return nil, ErrUnsafePerRecordWrite
}

// DeleteDNSRecord is retained for compatibility, but never reads or writes DNS.
func (c *Connector) DeleteDNSRecord(_ context.Context, _ string, _ int) error {
	return ErrUnsafePerRecordWrite
}

func (c *Connector) SetCustomNameservers(ctx context.Context, domainName string, nameservers []string) (*DomainNameserverUpdate, error) {
	return c.backend.SetCustomNameservers(ctx, domainName, nameservers)
}

// DomainsJSON returns the domain list as a JSON string (used by MCP tools).
func (c *Connector) DomainsJSON(ctx context.Context) (string, error) {
	domains, err := c.backend.ListDomains(ctx)
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
	status, err := c.backend.GetDomainStatus(ctx, domainName)
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

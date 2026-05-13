package namecheap

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const apiBaseURL = "https://api.namecheap.com/xml.response"

// Client wraps HTTP calls to the Namecheap XML API.
type Client struct {
	http     *http.Client
	apiUser  string
	apiKey   string
	username string
	clientIP string
}

// NewClient creates a Namecheap API client.
func NewClient(apiUser, apiKey, username, clientIP string) *Client {
	return &Client{
		http:     &http.Client{},
		apiUser:  apiUser,
		apiKey:   apiKey,
		username: username,
		clientIP: clientIP,
	}
}

// --- XML response structs ---

type domainsGetListResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainGetListResult struct {
			Domains []xmlDomain `xml:"Domain"`
		} `xml:"DomainGetListResult"`
	} `xml:"CommandResponse"`
}

type xmlDomain struct {
	ID         string `xml:"ID,attr"`
	Name       string `xml:"Name,attr"`
	Expires    string `xml:"Expires,attr"`
	IsExpired  string `xml:"IsExpired,attr"`
	IsLocked   string `xml:"IsLocked,attr"`
	AutoRenew  string `xml:"AutoRenew,attr"`
	WhoisGuard string `xml:"WhoisGuard,attr"`
}

type domainsGetInfoResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainGetInfoResult struct {
			Status        string `xml:"Status,attr"`
			DomainName    string `xml:"DomainName,attr"`
			DomainDetails struct {
				ExpiredDate string `xml:"ExpiredDate"`
			} `xml:"DomainDetails"`
			DNSDetails struct {
				NameServers []string `xml:"Nameserver"`
			} `xml:"DnsDetails"`
		} `xml:"DomainGetInfoResult"`
	} `xml:"CommandResponse"`
}

type dnsGetHostsResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainDNSGetHostsResult struct {
			Hosts []xmlHost `xml:"host"`
		} `xml:"DomainDNSGetHostsResult"`
	} `xml:"CommandResponse"`
}

type xmlHost struct {
	HostID  string `xml:"HostId,attr"`
	Name    string `xml:"Name,attr"`
	Type    string `xml:"Type,attr"`
	Address string `xml:"Address,attr"`
	TTL     string `xml:"TTL,attr"`
	MXPref  string `xml:"MXPref,attr"`
}

// --- API methods ---

// ListDomains returns all domains in the account.
func (c *Client) ListDomains(ctx context.Context) ([]Domain, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.getList", nil)
	if err != nil {
		return nil, fmt.Errorf("namecheap list domains: %w", err)
	}

	var resp domainsGetListResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap list domains: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, fmt.Errorf("namecheap list domains: %s", extractError(resp.Errors.Error))
	}

	domains := make([]Domain, len(resp.CommandResponse.DomainGetListResult.Domains))
	for i, d := range resp.CommandResponse.DomainGetListResult.Domains {
		domains[i] = Domain{
			Name:       d.Name,
			Expires:    d.Expires,
			IsExpired:  parseBool(d.IsExpired),
			IsLocked:   parseBool(d.IsLocked),
			AutoRenew:  parseBool(d.AutoRenew),
			WhoisGuard: d.WhoisGuard,
		}
	}
	return domains, nil
}

// GetDomainStatus returns detailed info for a single domain.
func (c *Client) GetDomainStatus(ctx context.Context, domain string) (*DomainStatus, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.getInfo", map[string]string{
		"DomainName": domain,
	})
	if err != nil {
		return nil, fmt.Errorf("namecheap domain status: %w", err)
	}

	var resp domainsGetInfoResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap domain status: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, fmt.Errorf("namecheap domain status: %s", extractError(resp.Errors.Error))
	}

	info := resp.CommandResponse.DomainGetInfoResult
	registered := strings.EqualFold(info.Status, "Ok") || strings.EqualFold(info.Status, "Active")

	return &DomainStatus{
		Domain:      info.DomainName,
		Registered:  registered,
		Expires:     info.DomainDetails.ExpiredDate,
		NameServers: info.DNSDetails.NameServers,
	}, nil
}

// ListDNSRecords returns DNS host records for a domain.
// The Namecheap API requires the domain split into SLD and TLD.
func (c *Client) ListDNSRecords(ctx context.Context, sld, tld string) ([]DNSRecord, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.dns.getHosts", map[string]string{
		"SLD": sld,
		"TLD": tld,
	})
	if err != nil {
		return nil, fmt.Errorf("namecheap list dns: %w", err)
	}

	var resp dnsGetHostsResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap list dns: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, fmt.Errorf("namecheap list dns: %s", extractError(resp.Errors.Error))
	}

	hosts := resp.CommandResponse.DomainDNSGetHostsResult.Hosts
	records := make([]DNSRecord, len(hosts))
	for i, h := range hosts {
		id, _ := strconv.Atoi(h.HostID)
		ttl, _ := strconv.Atoi(h.TTL)
		mxPref, _ := strconv.Atoi(h.MXPref)
		records[i] = DNSRecord{
			ID:     id,
			Type:   h.Type,
			Host:   h.Name,
			Value:  h.Address,
			TTL:    ttl,
			MXPref: mxPref,
		}
	}
	return records, nil
}

// SetDNSRecords replaces the full DNS host record set for a domain.
func (c *Client) SetDNSRecords(ctx context.Context, sld, tld string, records []DNSRecord) error {
	params := map[string]string{
		"SLD": sld,
		"TLD": tld,
	}
	for i, record := range records {
		n := strconv.Itoa(i + 1)
		params["HostName"+n] = record.Host
		params["RecordType"+n] = record.Type
		params["Address"+n] = record.Value
		if record.TTL > 0 {
			params["TTL"+n] = strconv.Itoa(record.TTL)
		}
		if record.MXPref > 0 {
			params["MXPref"+n] = strconv.Itoa(record.MXPref)
		}
	}

	body, err := c.doRequest(ctx, "namecheap.domains.dns.setHosts", params)
	if err != nil {
		return fmt.Errorf("namecheap set dns: %w", err)
	}

	var resp struct {
		XMLName xml.Name `xml:"ApiResponse"`
		Status  string   `xml:"Status,attr"`
		Errors  struct {
			Error []struct {
				Number  string `xml:"Number,attr"`
				Message string `xml:",chardata"`
			} `xml:"Error"`
		} `xml:"Errors"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("namecheap set dns: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return fmt.Errorf("namecheap set dns: %s", extractError(resp.Errors.Error))
	}
	return nil
}

// --- internal helpers ---

func (c *Client) doRequest(ctx context.Context, command string, extra map[string]string) ([]byte, error) {
	params := url.Values{}
	params.Set("ApiUser", c.apiUser)
	params.Set("ApiKey", c.apiKey)
	params.Set("UserName", c.username)
	params.Set("ClientIp", c.clientIP)
	params.Set("Command", command)
	for k, v := range extra {
		params.Set(k, v)
	}

	reqURL := apiBaseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func parseBool(s string) bool {
	return strings.EqualFold(s, "true")
}

func extractError(errors []struct {
	Number  string `xml:"Number,attr"`
	Message string `xml:",chardata"`
}) string {
	if len(errors) == 0 {
		return "unknown API error"
	}
	msgs := make([]string, len(errors))
	for i, e := range errors {
		msgs[i] = strings.TrimSpace(e.Message)
	}
	return strings.Join(msgs, "; ")
}

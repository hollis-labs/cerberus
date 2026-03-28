package mcp

import (
	"context"
	"encoding/json"

	cfconn "github.com/chrispian/cerberus/internal/connector/cloudflare"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusCloudflareZonesTool creates the cerberus_cloudflare_zones tool.
func NewCerberusCloudflareZonesTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_zones",
		Description: "Lists all Cloudflare zones in the account.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			cf, err := cfconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return cf.ZonesJSON(context.Background())
		},
	}
}

// NewCerberusCloudflareDNSListTool creates the cerberus_cloudflare_dns_list tool.
func NewCerberusCloudflareDNSListTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_list",
		Description: "Lists DNS records for a Cloudflare zone.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"zone_id": map[string]interface{}{
					"type":        "string",
					"description": "Cloudflare zone ID.",
				},
			},
			"required": []string{"zone_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			zoneID, _ := args["zone_id"].(string)

			cf, err := cfconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return cf.DNSRecordsJSON(context.Background(), zoneID)
		},
	}
}

// NewCerberusCloudflareDNSCreateTool creates the cerberus_cloudflare_dns_create tool.
func NewCerberusCloudflareDNSCreateTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_create",
		Description: "Creates a DNS record in a Cloudflare zone.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"zone_id": map[string]interface{}{
					"type":        "string",
					"description": "Cloudflare zone ID.",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "DNS record type (A, AAAA, CNAME, MX, TXT).",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "DNS record name.",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "DNS record content (e.g. IP address).",
				},
				"ttl": map[string]interface{}{
					"type":        "integer",
					"description": "TTL in seconds (1 = automatic).",
				},
				"proxied": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether the record is proxied through Cloudflare.",
				},
			},
			"required": []string{"zone_id", "type", "name", "content"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			zoneID, _ := args["zone_id"].(string)
			recType, _ := args["type"].(string)
			name, _ := args["name"].(string)
			content, _ := args["content"].(string)

			rec := cfconn.DNSRecord{
				Type:    recType,
				Name:    name,
				Content: content,
			}

			if ttl, ok := args["ttl"].(float64); ok && ttl > 0 {
				rec.TTL = int(ttl)
			}
			if proxied, ok := args["proxied"].(bool); ok {
				rec.Proxied = proxied
			}

			cf, err := cfconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			created, err := cf.CreateDNSRecord(context.Background(), zoneID, rec)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			data, err := json.MarshalIndent(created, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

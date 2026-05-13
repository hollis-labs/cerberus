package mcp

import (
	"context"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusCloudflareZonesTool creates the cerberus_cloudflare_zones tool.
func NewCerberusCloudflareZonesTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_zones",
		Description: "Lists all Cloudflare zones in the account.",
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "cloudflare", "list_zones", nil, false, false)
		},
	}
}

// NewCerberusCloudflareDNSListTool creates the cerberus_cloudflare_dns_list tool.
func NewCerberusCloudflareDNSListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_list",
		Description: "Lists DNS records for a Cloudflare zone.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"zone_id": map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
			},
			"required": []string{"zone_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "cloudflare", "list_dns_records", map[string]any{"zone_id": stringArg(args, "zone_id")}, false, false)
		},
	}
}

// NewCerberusCloudflareDNSCreateTool creates the cerberus_cloudflare_dns_create tool.
func NewCerberusCloudflareDNSCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_create",
		Description: "Creates a DNS record in a Cloudflare zone.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"zone_id":      map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
				"type":         map[string]interface{}{"type": "string", "description": "DNS record type (A, AAAA, CNAME, MX, TXT)."},
				"name":         map[string]interface{}{"type": "string", "description": "DNS record name."},
				"content":      map[string]interface{}{"type": "string", "description": "DNS record content (e.g. IP address)."},
				"ttl":          map[string]interface{}{"type": "integer", "description": "TTL in seconds (1 = automatic)."},
				"proxied":      map[string]interface{}{"type": "boolean", "description": "Whether the record is proxied through Cloudflare."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the DNS change without sending it to Cloudflare."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive DNS change."},
			},
			"required": []string{"zone_id", "type", "name", "content"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			cfg := map[string]any{
				"zone_id": stringArg(args, "zone_id"),
				"type":    stringArg(args, "type"),
				"name":    stringArg(args, "name"),
				"content": stringArg(args, "content"),
			}
			if ttl, ok := args["ttl"].(float64); ok && ttl >= 0 {
				cfg["ttl"] = int(ttl)
			}
			if proxied, ok := args["proxied"].(bool); ok {
				cfg["proxied"] = proxied
			}
			return executeConnectorMCP(client, "cloudflare", "create_dns_record", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusCloudflareDNSDeleteTool creates the cerberus_cloudflare_dns_delete tool.
func NewCerberusCloudflareDNSDeleteTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_delete",
		Description: "Deletes a DNS record from a Cloudflare zone.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"zone_id":      map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
				"record_id":    map[string]interface{}{"type": "string", "description": "Cloudflare DNS record ID."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the DNS change without sending it to Cloudflare."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive DNS change."},
			},
			"required": []string{"zone_id", "record_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "cloudflare", "delete_dns_record", map[string]any{
				"zone_id":   stringArg(args, "zone_id"),
				"record_id": stringArg(args, "record_id"),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

func executeConnectorMCP(client cerbapi.Client, connectorID, operation string, cfg map[string]any, dryRun, acknowledged bool) (string, error) {
	result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
		Connector:    connectorID,
		Operation:    operation,
		Config:       cfg,
		DryRun:       dryRun,
		Acknowledged: acknowledged,
	})
	if err != nil {
		return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil
	}
	return marshalConnectorData(result.Data)
}

func boolArg(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

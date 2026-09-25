package mcp

import (
	"context"
	"math"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusCloudflareZonesTool creates the cerberus_cloudflare_zones tool.
func NewCerberusCloudflareZonesTool(client cerbapi.Client) Tool {
	return Tool{
		Name:         "cerberus_cloudflare_zones",
		Description:  "List Cloudflare zones.",
		InputSchema:  emptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "cloudflare", "list_zones", nil, false, false)
		},
	}
}

// NewCerberusCloudflareZoneCreateTool creates the cerberus_cloudflare_zone_create tool.
func NewCerberusCloudflareZoneCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_zone_create",
		Description: "Create a Cloudflare zone.",
		InputSchema: objectSchema(map[string]interface{}{
			"account_id":   map[string]interface{}{"type": "string", "description": "Cloudflare account ID."},
			"name":         map[string]interface{}{"type": "string", "description": "Zone name, such as example.com."},
			"type":         map[string]interface{}{"type": "string", "description": "Zone type.", "enum": []string{"full", "partial"}},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "account_id", "name"),
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  false,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			cfg := map[string]any{
				"account_id": stringArg(args, "account_id"),
				"name":       stringArg(args, "name"),
			}
			if zoneType := stringArg(args, "type"); zoneType != "" {
				cfg["type"] = zoneType
			}
			return executeConnectorMCP(ctx, client, "cloudflare", "create_zone", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusCloudflareDNSListTool creates the cerberus_cloudflare_dns_list tool.
func NewCerberusCloudflareDNSListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_list",
		Description: "List Cloudflare DNS records for a zone.",
		InputSchema: objectSchema(map[string]interface{}{
			"zone_id": map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
		}, "zone_id"),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "cloudflare", "list_dns_records", map[string]any{"zone_id": stringArg(args, "zone_id")}, false, false)
		},
	}
}

// NewCerberusCloudflareDNSCreateTool creates the cerberus_cloudflare_dns_create tool.
func NewCerberusCloudflareDNSCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_create",
		Description: "Create a Cloudflare DNS record.",
		InputSchema: objectSchema(map[string]interface{}{
			"zone_id":      map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
			"type":         map[string]interface{}{"type": "string", "description": "DNS record type, such as A, AAAA, CNAME, MX, or TXT."},
			"name":         map[string]interface{}{"type": "string", "description": "DNS record name."},
			"content":      map[string]interface{}{"type": "string", "description": "DNS record value."},
			"ttl":          map[string]interface{}{"type": "integer", "description": "TTL in seconds. Use 1 for automatic."},
			"priority":     map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 65535, "description": "MX priority; required for MX records. Zero is valid."},
			"proxied":      map[string]interface{}{"type": "boolean", "description": "Proxy through Cloudflare."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "zone_id", "type", "name", "content"),
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  false,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
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
			if raw, present := args["priority"]; present {
				priority, ok := raw.(float64)
				if integer, isInt := raw.(int); isInt {
					priority, ok = float64(integer), true
				}
				if !ok || math.IsNaN(priority) || priority < 0 || priority > 65535 || math.Trunc(priority) != priority {
					return toolResult(lifecycleResult{Success: false, Error: "priority must be an integer between 0 and 65535"})
				}
				cfg["priority"] = int(priority)
			}
			return executeConnectorMCP(ctx, client, "cloudflare", "create_dns_record", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusCloudflareDNSDeleteTool creates the cerberus_cloudflare_dns_delete tool.
func NewCerberusCloudflareDNSDeleteTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_cloudflare_dns_delete",
		Description: "Delete a Cloudflare DNS record.",
		InputSchema: objectSchema(map[string]interface{}{
			"zone_id":      map[string]interface{}{"type": "string", "description": "Cloudflare zone ID."},
			"record_id":    map[string]interface{}{"type": "string", "description": "Cloudflare DNS record ID."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "zone_id", "record_id"),
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "cloudflare", "delete_dns_record", map[string]any{
				"zone_id":   stringArg(args, "zone_id"),
				"record_id": stringArg(args, "record_id"),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

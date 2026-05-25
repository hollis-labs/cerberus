package mcp

import (
	"context"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusDomainListTool creates the cerberus_domain_list tool.
func NewCerberusDomainListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_domain_list",
		Description: "List Namecheap domains.",
		InputSchema: emptyObjectSchema(),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return executeConnectorMCP(ctx, client, "namecheap", "list_domains", nil, false, false)
		},
	}
}

// NewCerberusDomainStatusTool creates the cerberus_domain_status tool.
func NewCerberusDomainStatusTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_domain_status",
		Description: "Get registration status and nameservers for a domain.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain": map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
		}, "domain"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return executeConnectorMCP(ctx, client, "namecheap", "get_domain_status", map[string]any{"domain": stringArg(args, "domain")}, false, false)
		},
	}
}

// NewCerberusDNSListTool creates the cerberus_dns_list tool.
func NewCerberusDNSListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_list",
		Description: "List Namecheap DNS records for a domain.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain": map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
		}, "domain"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return executeConnectorMCP(ctx, client, "namecheap", "list_dns_records", map[string]any{"domain": stringArg(args, "domain")}, false, false)
		},
	}
}

// NewCerberusDNSCreateTool creates the cerberus_dns_create tool.
func NewCerberusDNSCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_create",
		Description: "Create a Namecheap DNS record.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain":       map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
			"type":         map[string]interface{}{"type": "string", "description": "DNS record type."},
			"host":         map[string]interface{}{"type": "string", "description": "Host name, such as @, www, or api."},
			"value":        map[string]interface{}{"type": "string", "description": "DNS record value."},
			"ttl":          map[string]interface{}{"type": "integer", "description": "TTL in seconds."},
			"mx_pref":      map[string]interface{}{"type": "integer", "description": "MX preference for MX records."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "domain", "type", "host", "value"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			cfg := map[string]any{
				"domain": stringArg(args, "domain"),
				"type":   stringArg(args, "type"),
				"host":   stringArg(args, "host"),
				"value":  stringArg(args, "value"),
			}
			if ttl, ok := args["ttl"].(float64); ok {
				cfg["ttl"] = int(ttl)
			}
			if mxPref, ok := args["mx_pref"].(float64); ok {
				cfg["mx_pref"] = int(mxPref)
			}
			return executeConnectorMCP(ctx, client, "namecheap", "create_dns_record", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusDNSDeleteTool creates the cerberus_dns_delete tool.
func NewCerberusDNSDeleteTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_delete",
		Description: "Delete a Namecheap DNS record.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain":       map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
			"record_id":    map[string]interface{}{"type": "integer", "description": "Namecheap record ID."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "domain", "record_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return executeConnectorMCP(ctx, client, "namecheap", "delete_dns_record", map[string]any{
				"domain":    stringArg(args, "domain"),
				"record_id": intArg(args, "record_id", 0),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusNameserversSetTool creates the cerberus_nameservers_set tool.
func NewCerberusNameserversSetTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_nameservers_set",
		Description: "Set custom nameservers for a Namecheap domain.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain": map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
			"nameservers": map[string]interface{}{
				"type":        "array",
				"description": "Nameservers to assign.",
				"items":       map[string]interface{}{"type": "string"},
				"minItems":    2,
			},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "domain", "nameservers"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			cfg := map[string]any{
				"domain":      stringArg(args, "domain"),
				"nameservers": args["nameservers"],
			}
			return executeConnectorMCP(ctx, client, "namecheap", "set_custom_nameservers", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

func stringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}

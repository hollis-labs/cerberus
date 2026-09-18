package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/connector/namecheap"
)

// NewCerberusDomainListTool creates the cerberus_domain_list tool.
func NewCerberusDomainListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:         "cerberus_domain_list",
		Description:  "List Namecheap domains.",
		InputSchema:  emptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
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
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
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
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return executeConnectorMCP(ctx, client, "namecheap", "list_dns_records", map[string]any{"domain": stringArg(args, "domain")}, false, false)
		},
	}
}

// NewCerberusDNSCreateTool creates the cerberus_dns_create tool.
func NewCerberusDNSCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_create",
		Description: "Disabled: per-record Namecheap writes can silently delete hidden records. Use cerberus_set_dns_record_set with an authoritative whole-zone set.",
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
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  false,
		OpenWorldHint:   false,
		Handler: func(_ context.Context, _ map[string]interface{}) (any, error) {
			return marshalResult(lifecycleResult{Success: false, Error: namecheap.ErrUnsafePerRecordWrite.Error()}), nil
		},
	}
}

// NewCerberusDNSDeleteTool creates the cerberus_dns_delete tool.
func NewCerberusDNSDeleteTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_delete",
		Description: "Disabled: per-record Namecheap writes can silently delete hidden records. Use cerberus_set_dns_record_set with an authoritative whole-zone set.",
		InputSchema: objectSchema(map[string]interface{}{
			"domain":       map[string]interface{}{"type": "string", "description": "Domain name, such as example.com."},
			"record_id":    map[string]interface{}{"type": "integer", "description": "Namecheap record ID."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "domain", "record_id"),
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(_ context.Context, _ map[string]interface{}) (any, error) {
			return marshalResult(lifecycleResult{Success: false, Error: namecheap.ErrUnsafePerRecordWrite.Error()}), nil
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
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
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

// NewCerberusDNSRecordSetTools exposes the email-aware whole-zone operations.
func NewCerberusDNSRecordSetTools(client cerbapi.Client) []Tool {
	var result []Tool
	for _, op := range namecheap.Definition().Operations {
		if op.Name != "get_dns_record_set" && op.Name != "set_dns_record_set" {
			continue
		}
		operation := op.Name
		properties := op.InputSchema["properties"].(map[string]any)
		if op.Destructive {
			properties["dry_run"] = map[string]any{"type": "boolean", "description": "Preview without changing DNS or email routing."}
			properties["acknowledged"] = map[string]any{"type": "boolean", "description": "Acknowledge replacing the full zone and explicitly setting email routing; omitted hosts are deleted."}
		}
		result = append(result, Tool{
			Name:            "cerberus_" + operation,
			Description:     op.Description,
			InputSchema:     op.InputSchema,
			ReadOnlyHint:    !op.Destructive,
			DestructiveHint: op.Destructive,
			IdempotentHint:  op.Destructive,
			OpenWorldHint:   false,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				config := map[string]any{"domain": stringArg(args, "domain")}
				if operation == "set_dns_record_set" {
					config["email_type"] = stringArg(args, "email_type")
					config["records"] = args["records"]
				}
				return executeConnectorMCP(ctx, client, "namecheap", operation, config, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
			},
		})
	}
	return result
}

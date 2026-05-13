package mcp

import "github.com/chrispian/cerberus/internal/cerbapi"

// NewCerberusDomainListTool creates the cerberus_domain_list tool.
func NewCerberusDomainListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_domain_list",
		Description: "Lists all domains in the Namecheap account.",
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "namecheap", "list_domains", nil, false, false)
		},
	}
}

// NewCerberusDomainStatusTool creates the cerberus_domain_status tool.
func NewCerberusDomainStatusTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_domain_status",
		Description: "Returns registration status and nameservers for a domain.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain": map[string]interface{}{"type": "string", "description": "Domain name (e.g. example.com)."},
			},
			"required": []string{"domain"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "namecheap", "get_domain_status", map[string]any{"domain": stringArg(args, "domain")}, false, false)
		},
	}
}

// NewCerberusDNSListTool creates the cerberus_dns_list tool.
func NewCerberusDNSListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_list",
		Description: "Lists DNS records for a domain via Namecheap.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain": map[string]interface{}{"type": "string", "description": "Domain name (e.g. example.com)."},
			},
			"required": []string{"domain"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "namecheap", "list_dns_records", map[string]any{"domain": stringArg(args, "domain")}, false, false)
		},
	}
}

// NewCerberusDNSCreateTool creates the cerberus_dns_create tool.
func NewCerberusDNSCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_create",
		Description: "Creates a DNS record for a Namecheap domain.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain":       map[string]interface{}{"type": "string", "description": "Domain name (e.g. example.com)."},
				"type":         map[string]interface{}{"type": "string", "description": "DNS record type."},
				"host":         map[string]interface{}{"type": "string", "description": "Host name such as @, www, or api."},
				"value":        map[string]interface{}{"type": "string", "description": "DNS record value."},
				"ttl":          map[string]interface{}{"type": "integer", "description": "TTL in seconds."},
				"mx_pref":      map[string]interface{}{"type": "integer", "description": "MX preference for MX records."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the DNS change without sending it to Namecheap."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive DNS change."},
			},
			"required": []string{"domain", "type", "host", "value"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
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
			return executeConnectorMCP(client, "namecheap", "create_dns_record", cfg, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

// NewCerberusDNSDeleteTool creates the cerberus_dns_delete tool.
func NewCerberusDNSDeleteTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_dns_delete",
		Description: "Deletes a DNS record for a Namecheap domain by record ID.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain":       map[string]interface{}{"type": "string", "description": "Domain name (e.g. example.com)."},
				"record_id":    map[string]interface{}{"type": "integer", "description": "Namecheap record ID."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the DNS change without sending it to Namecheap."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive DNS change."},
			},
			"required": []string{"domain", "record_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			return executeConnectorMCP(client, "namecheap", "delete_dns_record", map[string]any{
				"domain":    stringArg(args, "domain"),
				"record_id": intArg(args, "record_id", 0),
			}, boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	}
}

func stringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}

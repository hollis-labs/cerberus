package mcp

import (
	"context"

	ncconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusDomainListTool creates the cerberus_domain_list tool.
func NewCerberusDomainListTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_domain_list",
		Description: "Lists all domains in the Namecheap account.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			nc, err := ncconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return nc.DomainsJSON(context.Background())
		},
	}
}

// NewCerberusDomainStatusTool creates the cerberus_domain_status tool.
func NewCerberusDomainStatusTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_domain_status",
		Description: "Returns registration status and nameservers for a domain.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain": map[string]interface{}{
					"type":        "string",
					"description": "Domain name (e.g. example.com).",
				},
			},
			"required": []string{"domain"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			domainName, _ := args["domain"].(string)

			nc, err := ncconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return nc.DomainStatusJSON(context.Background(), domainName)
		},
	}
}

// NewCerberusDNSListTool creates the cerberus_dns_list tool.
func NewCerberusDNSListTool(secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_dns_list",
		Description: "Lists DNS records for a domain via Namecheap.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain": map[string]interface{}{
					"type":        "string",
					"description": "Domain name (e.g. example.com).",
				},
			},
			"required": []string{"domain"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			domainName, _ := args["domain"].(string)

			nc, err := ncconn.New(secrets)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return nc.DNSRecordsJSON(context.Background(), domainName)
		},
	}
}

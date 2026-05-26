package mcp

import (
	"context"
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusConnectorListTool creates the cerberus_connector_list tool.
func NewCerberusConnectorListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_connector_list",
		Description: "List available connectors (budgeted envelope). Use cerberus_connector_describe for full schema and operations.",
		InputSchema: objectSchema(map[string]interface{}{
			"limit": limitSchemaProp(),
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			defs, err := client.ListConnectors(ctx)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}
			return budgetedList("cerberus_connector_list", defs, args, "%d connectors available."), nil
		},
	}
}

// NewCerberusConnectorDescribeTool creates the cerberus_connector_describe tool.
func NewCerberusConnectorDescribeTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_connector_describe",
		Description: "Get full schema and operations for one connector.",
		InputSchema: objectSchema(map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Connector ID, such as docker, github, cloudflare, or ssh."},
		}, "id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			id, _ := args["id"].(string)
			if id == "" {
				return marshalResult(lifecycleResult{Success: false, Error: `missing "id"`}), nil
			}
			defs, err := client.ListConnectors(ctx)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			for _, def := range defs {
				if def.ID != id {
					continue
				}
				data, err := json.MarshalIndent(def, "", "  ")
				if err != nil {
					return "", err
				}
				return string(data), nil
			}
			return marshalResult(lifecycleResult{Success: false, Error: "connector not found: " + id}), nil
		},
	}
}

package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusConnectorListTool creates the cerberus_connector_list tool.
func NewCerberusConnectorListTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_connector_list",
		Description: "List available connectors (budgeted envelope). Use cerberus_connector_describe for full schema and operations.",
		InputSchema: objectSchema(map[string]interface{}{
			"limit":  limitSchemaProp(),
			"offset": offsetSchemaProp(),
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			defs, err := client.ListConnectors(ctx)
			if err != nil {
				return connectorFailure(ctx, err)
			}
			return budgetedList("cerberus_connector_list", defs, args, "%d connectors total."), nil
		},
	})
}

// NewCerberusConnectorDescribeTool creates the cerberus_connector_describe tool.
func NewCerberusConnectorDescribeTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_connector_describe",
		Description: "Get full schema and operations for one connector.",
		InputSchema: objectSchema(map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Connector ID, such as docker, github, cloudflare, or ssh."},
		}, "id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			id, _ := args["id"].(string)
			if id == "" {
				return toolResult(lifecycleResult{Success: false, Error: `missing "id"`})
			}
			defs, err := client.ListConnectors(ctx)
			if err != nil {
				return connectorFailure(ctx, err)
			}
			for _, def := range defs {
				if def.ID != id {
					continue
				}
				data, err := redact.MarshalIndent(def, "", "  ")
				if err != nil {
					return "", err
				}
				return string(data), nil
			}
			return toolResult(lifecycleResult{Success: false, Error: "connector not found: " + id})
		},
	})
}

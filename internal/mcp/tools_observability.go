package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusHealthTool creates the cerberus_health tool.
func NewCerberusHealthTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_health",
		Description: "Get daemon and resource health. Optional resource_id filters to one resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional resource ID filter.",
			},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			filterID, _ := args["resource_id"].(string)
			h, err := client.Health(ctx, filterID)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(h, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})
}

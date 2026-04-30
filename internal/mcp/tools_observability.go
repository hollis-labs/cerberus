package mcp

import (
	"context"
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusHealthTool creates the cerberus_health tool.
func NewCerberusHealthTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_health",
		Description: "Returns daemon health plus v2 resource runtime health. When filtering, resource_id targets a specific v2 resource.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional v2 resource ID. If omitted, returns health for all resources.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			filterID, _ := args["resource_id"].(string)
			h, err := client.Health(context.Background(), filterID)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(h, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

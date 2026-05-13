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
		Description: "Lists external connector discovery metadata, including resource types, capabilities, config fields, secrets, and operations.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			defs, err := client.ListConnectors(context.Background())
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}
			data, err := json.MarshalIndent(defs, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusConnectorDescribeTool creates the cerberus_connector_describe tool.
func NewCerberusConnectorDescribeTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_connector_describe",
		Description: "Returns full discovery metadata for a specific external connector, including operation examples, destructive flags, and dry-run support.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string", "description": "Connector ID, such as docker, github, cloudflare, or ssh."},
			},
			"required": []string{"id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			id, _ := args["id"].(string)
			if id == "" {
				return marshalResult(lifecycleResult{Success: false, Error: `missing "id"`}), nil
			}
			defs, err := client.ListConnectors(context.Background())
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

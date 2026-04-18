package mcp

import (
	"context"
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusStatusTool creates the cerberus_status tool.
//
// The tool routes through a cerbapi.Client — either InProcessClient
// (when the daemon hosts its own MCP surface) or SocketClient (when
// the standalone `cerberus mcp` subprocess forwards to the daemon).
// The daemon side always re-parses config via its ServiceRegistry
// Reload() before returning results, so stale-config staleness is
// physically impossible regardless of which caller invoked the tool.
func NewCerberusStatusTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_status",
		Description: "Returns the current status of Cerberus-managed services. Optionally filter by service_id. Includes daemon protection and auto-restart state.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional service ID to filter. If omitted, returns all services.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			filterID, _ := args["service_id"].(string)
			ctx := context.Background()

			if filterID != "" {
				st, err := client.GetService(ctx, filterID)
				if err != nil {
					return "", err
				}
				data, err := json.MarshalIndent([]cerbapi.ServiceStatus{*st}, "", "  ")
				if err != nil {
					return "", err
				}
				return string(data), nil
			}

			list, err := client.ListServices(ctx)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(list, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

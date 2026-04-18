package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusLogsTool creates the cerberus_logs tool.
func NewCerberusLogsTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_logs",
		Description: "Returns the last N lines from a service's log file.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to read logs for.",
				},
				"lines": map[string]interface{}{
					"type":        "integer",
					"description": "Number of lines to return (default 50).",
				},
			},
			"required": []string{"service_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return "", fmt.Errorf("service_id is required")
			}
			lines := 50
			if l, ok := args["lines"].(float64); ok && l > 0 {
				lines = int(l)
			}
			ll, err := client.ServiceLogs(context.Background(), serviceID, lines)
			if err != nil {
				return "", err
			}
			return ll.Content, nil
		},
	}
}

// NewCerberusBuildTool creates the cerberus_build tool.
func NewCerberusBuildTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_build",
		Description: "Runs the build command for a service synchronously and returns the result.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to build.",
				},
			},
			"required": []string{"service_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return "", fmt.Errorf("service_id is required")
			}
			res, err := client.BuildService(context.Background(), serviceID)
			if err != nil {
				return "", err
			}
			// Preserve pre-CERB-2 output shape: buildResult had Output
			// instead of BuildOutput. Marshal into a local struct so
			// the wire format stays byte-stable for existing MCP
			// consumers.
			buildOut := struct {
				Success   bool   `json:"success"`
				ServiceID string `json:"service_id"`
				Output    string `json:"output,omitempty"`
				Error     string `json:"error,omitempty"`
			}{
				Success:   res.Success,
				ServiceID: res.ServiceID,
				Output:    res.BuildOutput,
				Error:     res.Error,
			}
			data, _ := json.MarshalIndent(buildOut, "", "  ")
			return string(data), nil
		},
	}
}

// NewCerberusHealthTool creates the cerberus_health tool.
func NewCerberusHealthTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_health",
		Description: "Returns health check results for one or all services, plus daemon monitor status.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional service ID. If omitted, returns health for all services.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			filterID, _ := args["service_id"].(string)
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

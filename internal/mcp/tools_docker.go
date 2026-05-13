package mcp

import (
	"context"
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusDockerPSTool creates the cerberus_docker_ps tool.
func NewCerberusDockerPSTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_docker_ps",
		Description: "Lists running Docker containers with their status, image, and ports.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "list_containers",
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return marshalConnectorData(result.Data)
		},
	}
}

// NewCerberusDockerLogsTool creates the cerberus_docker_logs tool.
func NewCerberusDockerLogsTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_docker_logs",
		Description: "Returns the last N lines of logs from a Docker container.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"container": map[string]interface{}{
					"type":        "string",
					"description": "Container name or ID.",
				},
				"lines": map[string]interface{}{
					"type":        "integer",
					"description": "Number of log lines to return (default 50).",
				},
			},
			"required": []string{"container"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			container, _ := args["container"].(string)
			lines := 50
			if l, ok := args["lines"].(float64); ok && l > 0 {
				lines = int(l)
			}

			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "logs",
				Config: map[string]any{
					"container": container,
					"lines":     lines,
				},
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}
			return marshalConnectorData(result.Data)
		},
	}
}

// NewCerberusDockerUpTool creates the cerberus_docker_up tool.
func NewCerberusDockerUpTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_docker_up",
		Description: "Starts a Docker container or Compose stack.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"container_name": map[string]interface{}{
					"type":        "string",
					"description": "Container name to start.",
				},
				"compose_file": map[string]interface{}{
					"type":        "string",
					"description": "Compose file path to bring up (runs docker compose up -d).",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			cfg := map[string]any{}
			if composeFile, ok := args["compose_file"].(string); ok && composeFile != "" {
				cfg["compose_file"] = composeFile
			}
			if name, ok := args["container_name"].(string); ok && name != "" {
				cfg["container"] = name
				cfg["id"] = name
				cfg["name"] = name
			}

			if cfg["container"] == nil && cfg["compose_file"] == nil {
				return marshalResult(lifecycleResult{Success: false, Error: "one of container_name or compose_file is required"}), nil
			}

			if _, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "start",
				Config:    cfg,
			}); err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			if cfg["compose_file"] != nil {
				return marshalResult(lifecycleResult{Success: true, Message: "compose stack started"}), nil
			}
			name, _ := cfg["container"].(string)
			return marshalResult(lifecycleResult{Success: true, ServiceID: name, Message: "container started"}), nil
		},
	}
}

// NewCerberusDockerDownTool creates the cerberus_docker_down tool.
func NewCerberusDockerDownTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_docker_down",
		Description: "Stops a Docker container or Compose stack.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"container_name": map[string]interface{}{
					"type":        "string",
					"description": "Container name to stop.",
				},
				"compose_file": map[string]interface{}{
					"type":        "string",
					"description": "Compose file path to bring down (runs docker compose down).",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			cfg := map[string]any{}
			if composeFile, ok := args["compose_file"].(string); ok && composeFile != "" {
				cfg["compose_file"] = composeFile
			}
			if name, ok := args["container_name"].(string); ok && name != "" {
				cfg["container"] = name
				cfg["id"] = name
				cfg["name"] = name
			}

			if cfg["container"] == nil && cfg["compose_file"] == nil {
				return marshalResult(lifecycleResult{Success: false, Error: "one of container_name or compose_file is required"}), nil
			}

			if _, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "stop",
				Config:    cfg,
			}); err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			if cfg["compose_file"] != nil {
				return marshalResult(lifecycleResult{Success: true, Message: "compose stack stopped"}), nil
			}
			name, _ := cfg["container"].(string)
			return marshalResult(lifecycleResult{Success: true, ServiceID: name, Message: "container stopped"}), nil
		},
	}
}

func marshalConnectorData(data any) (string, error) {
	if text, ok := data.(string); ok {
		return text, nil
	}
	if data == nil {
		return marshalResult(lifecycleResult{Success: true}), nil
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

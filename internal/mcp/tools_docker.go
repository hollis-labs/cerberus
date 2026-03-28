package mcp

import (
	"context"

	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
)

// NewCerberusDockerPSTool creates the cerberus_docker_ps tool.
func NewCerberusDockerPSTool() Tool {
	return Tool{
		Name:        "cerberus_docker_ps",
		Description: "Lists running Docker containers with their status, image, and ports.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			dc, err := dockerconn.New()
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return dc.ContainersJSON(context.Background())
		},
	}
}

// NewCerberusDockerLogsTool creates the cerberus_docker_logs tool.
func NewCerberusDockerLogsTool() Tool {
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

			dc, err := dockerconn.New()
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return dc.LogsJSON(context.Background(), container, lines)
		},
	}
}

// NewCerberusDockerUpTool creates the cerberus_docker_up tool.
func NewCerberusDockerUpTool() Tool {
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
			dc, err := dockerconn.New()
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			ctx := context.Background()

			// Compose file mode
			if composeFile, ok := args["compose_file"].(string); ok && composeFile != "" {
				if err := dc.ComposeUp(ctx, composeFile); err != nil {
					return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
				}
				return marshalResult(lifecycleResult{Success: true, Message: "compose stack started"}), nil
			}

			// Container name mode
			name, _ := args["container_name"].(string)
			if name == "" {
				return marshalResult(lifecycleResult{Success: false, Error: "one of container_name or compose_file is required"}), nil
			}
			if err := dc.StartContainer(ctx, name); err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalResult(lifecycleResult{Success: true, ServiceID: name, Message: "container started"}), nil
		},
	}
}

// NewCerberusDockerDownTool creates the cerberus_docker_down tool.
func NewCerberusDockerDownTool() Tool {
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
			dc, err := dockerconn.New()
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			ctx := context.Background()

			// Compose file mode
			if composeFile, ok := args["compose_file"].(string); ok && composeFile != "" {
				if err := dc.ComposeDown(ctx, composeFile); err != nil {
					return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
				}
				return marshalResult(lifecycleResult{Success: true, Message: "compose stack stopped"}), nil
			}

			// Container name mode
			name, _ := args["container_name"].(string)
			if name == "" {
				return marshalResult(lifecycleResult{Success: false, Error: "one of container_name or compose_file is required"}), nil
			}
			if err := dc.StopContainer(ctx, name); err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalResult(lifecycleResult{Success: true, ServiceID: name, Message: "container stopped"}), nil
		},
	}
}

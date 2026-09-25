package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusDockerPSTool creates the cerberus_docker_ps tool.
func NewCerberusDockerPSTool(client cerbapi.Client) Tool {
	return Tool{
		Name:         "cerberus_docker_ps",
		Description:  "List running Docker containers, on this machine or on a remote Docker host.",
		InputSchema:  objectSchema(dockerTargetProperties(nil)),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "list_containers",
				Config:    dockerTargetConfig(args, nil),
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
		Description: "Get recent logs for a Docker container, on this machine or on a remote Docker host.",
		InputSchema: objectSchema(dockerTargetProperties(map[string]interface{}{
			"container": map[string]interface{}{
				"type":        "string",
				"description": "Container name or ID.",
			},
			"lines": map[string]interface{}{
				"type":        "integer",
				"description": "Number of log lines to return. Default 50.",
			},
		}), "container"),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			container, _ := args["container"].(string)
			lines := 50
			if l, ok := args["lines"].(float64); ok && l > 0 {
				lines = int(l)
			}

			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "logs",
				Config: dockerTargetConfig(args, map[string]any{
					"container": container,
					"lines":     lines,
				}),
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}
			logs, _ := result.Data.(string)
			return marshalConnectorData(struct {
				Container string `json:"container"`
				Lines     int    `json:"lines"`
				Output    string `json:"output"`
			}{
				Container: container,
				Lines:     lines,
				Output:    logs,
			})
		},
	}
}

// NewCerberusDockerUpTool creates the cerberus_docker_up tool.
func NewCerberusDockerUpTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_docker_up",
		Description: "Start a Docker container or Compose stack, on this machine or on a remote Docker host.",
		InputSchema: map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"properties": dockerTargetProperties(map[string]interface{}{
				"container_name": map[string]interface{}{
					"type":        "string",
					"description": "Container name to start.",
				},
				"compose_file": map[string]interface{}{
					"type":        "string",
					"description": "Compose file path to run with docker compose up -d.",
				},
			}),
			"oneOf": []map[string]interface{}{
				{"required": []string{"container_name"}},
				{"required": []string{"compose_file"}},
			},
		},
		ReadOnlyHint:    false,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			cfg := dockerTargetConfig(args, map[string]any{})
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

			if _, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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
		Description: "Stop a Docker container (docker stop) or Compose stack (docker compose stop), on this machine or on a remote Docker host. This does not remove the stack: containers and networks are kept and cerberus_docker_up starts it again. Removal is the docker connector's destroy operation, which requires acknowledgment.",
		InputSchema: map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"properties": dockerTargetProperties(map[string]interface{}{
				"container_name": map[string]interface{}{
					"type":        "string",
					"description": "Container name to stop.",
				},
				"compose_file": map[string]interface{}{
					"type":        "string",
					"description": "Compose file path to run with docker compose stop.",
				},
			}),
			"oneOf": []map[string]interface{}{
				{"required": []string{"container_name"}},
				{"required": []string{"compose_file"}},
			},
		},
		ReadOnlyHint:    false,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			cfg := dockerTargetConfig(args, map[string]any{})
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

			if _, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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

// dockerTargetProperties adds the remote-host parameters to a Docker tool's
// input schema. They are named docker_host and docker_context rather than host
// and context because an agent reading a tool list has no connector prefix to
// disambiguate them from the container's own host.
func dockerTargetProperties(properties map[string]interface{}) map[string]interface{} {
	merged := map[string]interface{}{
		"docker_host": map[string]interface{}{
			"type":        "string",
			"description": "Docker daemon to target, as a DOCKER_HOST value (ssh://user@host, tcp://host:2376). Omit for the Docker daemon on the machine running Cerberus.",
		},
		"docker_context": map[string]interface{}{
			"type":        "string",
			"description": "Docker context name to target. Mutually exclusive with docker_host.",
		},
	}
	for key, value := range properties {
		merged[key] = value
	}
	return merged
}

// dockerTargetConfig folds the remote-host tool arguments into an operation's
// config under the connector's own key names.
func dockerTargetConfig(args map[string]interface{}, cfg map[string]any) map[string]any {
	host, _ := args["docker_host"].(string)
	dockerContext, _ := args["docker_context"].(string)
	if host == "" && dockerContext == "" {
		return cfg
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if host != "" {
		cfg["host"] = host
	}
	if dockerContext != "" {
		cfg["context"] = dockerContext
	}
	return cfg
}

func marshalConnectorData(data any) (string, error) {
	if text, ok := data.(string); ok {
		return redact.Text(text), nil
	}
	if data == nil {
		return marshalResult(lifecycleResult{Success: true}), nil
	}
	out, err := redact.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

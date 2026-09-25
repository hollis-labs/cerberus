package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// Docker tools reach the Docker daemon on the machine running Cerberus, or a
// declared docker resource by id. They take no DOCKER_HOST, docker context or
// compose file: those are ad-hoc targets, available only from the operator's
// shell (`cerberus docker … --host/--context/-f`), and the socket refuses
// them. A remote daemon or a stack an agent should operate is declared as a
// `type: container`, `connector: docker` resource.

// NewCerberusDockerPSTool creates the cerberus_docker_ps tool.
func NewCerberusDockerPSTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_docker_ps",
		Description: "List running Docker containers on the machine running Cerberus.",
		InputSchema: objectSchema(map[string]interface{}{}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "list_containers",
			})
			if err != nil {
				return connectorFailure(ctx, err)
			}

			return marshalConnectorData(result.Data)
		},
	})
}

// NewCerberusDockerLogsTool creates the cerberus_docker_logs tool.
func NewCerberusDockerLogsTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_docker_logs",
		Description: "Get recent logs for a Docker container on the machine running Cerberus.",
		InputSchema: objectSchema(map[string]interface{}{
			"container": map[string]interface{}{
				"type":        "string",
				"description": "Container name or ID.",
			},
			"lines": map[string]interface{}{
				"type":        "integer",
				"description": "Number of log lines to return. Default 50.",
			},
		}, "container"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			container, _ := args["container"].(string)
			lines := 50
			if l, ok := args["lines"].(float64); ok && l > 0 {
				lines = int(l)
			}

			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector: "docker",
				Operation: "logs",
				Config: map[string]any{
					"container": container,
					"lines":     lines,
				},
			})
			if err != nil {
				return connectorFailure(ctx, err)
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
	})
}

// NewCerberusDockerUpTool creates the cerberus_docker_up tool.
func NewCerberusDockerUpTool(client cerbapi.Client) Tool {
	return newDockerLifecycleTool(client, "cerberus_docker_up", "start",
		"Start a local Docker container by name, or a declared docker resource (a container or a Compose stack, local or remote) by resource_id. Requires acknowledged=true.",
		"started")
}

// NewCerberusDockerDownTool creates the cerberus_docker_down tool.
func NewCerberusDockerDownTool(client cerbapi.Client) Tool {
	return newDockerLifecycleTool(client, "cerberus_docker_down", "stop",
		"Stop a local Docker container by name (docker stop), or a declared docker resource by resource_id (a Compose stack is stopped with docker compose stop). This does not remove anything: containers and networks are kept and cerberus_docker_up starts them again. Removal is cerberus_docker_destroy. Requires acknowledged=true.",
		"stopped")
}

// NewCerberusDockerDestroyTool creates the cerberus_docker_destroy tool.
func NewCerberusDockerDestroyTool(client cerbapi.Client) Tool {
	return newDockerLifecycleTool(client, "cerberus_docker_destroy", "destroy",
		"Remove a local Docker container by name (docker rm), or tear down a declared docker resource by resource_id (a Compose stack is removed with docker compose down, taking its containers and networks with it). This cannot be undone from Cerberus: cerberus_docker_up does not bring a removed stack back as it was. Requires acknowledged=true.",
		"destroyed")
}

func newDockerLifecycleTool(client cerbapi.Client, name, operation, description, verb string) Tool {
	return contractTool(Tool{
		Name:        name,
		Description: description,
		InputSchema: map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]interface{}{
				"container_name": map[string]interface{}{
					"type":        "string",
					"description": "Name of a container on the Docker daemon of the machine running Cerberus.",
				},
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "ID of a declared docker resource (type: container, connector: docker). Its compose file, container and Docker host come from the declaration.",
				},
				"acknowledged": map[string]interface{}{
					"type":        "boolean",
					"description": "Acknowledge the operation. Required: starting and stopping are lifecycle operations.",
				},
			},
			"oneOf": []map[string]interface{}{
				{"required": []string{"container_name"}},
				{"required": []string{"resource_id"}},
			},
		},
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			container, _ := args["container_name"].(string)
			var cfg map[string]any
			switch {
			case resourceID != "" && container != "":
				return toolResult(lifecycleResult{Success: false, Error: "pass container_name or resource_id, not both"})
			case resourceID != "":
				cfg = map[string]any{"resource": resourceID}
			case container != "":
				cfg = map[string]any{"container": container, "id": container, "name": container}
			default:
				return toolResult(lifecycleResult{Success: false, Error: "one of container_name or resource_id is required"})
			}

			if _, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector:    "docker",
				Operation:    operation,
				Config:       cfg,
				Acknowledged: boolArg(args, "acknowledged"),
			}); err != nil {
				return connectorFailure(ctx, err)
			}
			if resourceID != "" {
				return toolResult(lifecycleResult{Success: true, ServiceID: resourceID, Message: "resource " + verb})
			}
			return toolResult(lifecycleResult{Success: true, ServiceID: container, Message: "container " + verb})
		},
	})
}

package mcp

import (
	"context"
	"fmt"

	"github.com/chrispian/cerberus/internal/config"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusSSHExecTool creates the cerberus_ssh_exec tool.
func NewCerberusSSHExecTool(cfg *config.ConfigV2, secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_ssh_exec",
		Description: "Executes a command on a remote host via SSH. Returns stdout, stderr, and exit code.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "ID of the SSH resource to connect to.",
				},
				"command": map[string]interface{}{
					"type":        "string",
					"description": "Command to execute on the remote host.",
				},
			},
			"required": []string{"resource_id", "command"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			command, _ := args["command"].(string)

			res, err := findSSHResource(cfg, resourceID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			conn := sshconn.New(secrets)
			result, err := conn.ExecJSON(context.Background(), res, command)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return result, nil
		},
	}
}

// NewCerberusSSHStatusTool creates the cerberus_ssh_status tool.
func NewCerberusSSHStatusTool(cfg *config.ConfigV2, secrets domain.SecretProvider) Tool {
	return Tool{
		Name:        "cerberus_ssh_status",
		Description: "Checks connectivity and OS info for a remote host via SSH.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "ID of the SSH resource to check.",
				},
			},
			"required": []string{"resource_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)

			res, err := findSSHResource(cfg, resourceID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			conn := sshconn.New(secrets)
			result, err := conn.HostStatusJSON(context.Background(), res)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr // MCP tools embed errors in JSON response
			}

			return result, nil
		},
	}
}

// findSSHResource looks up a resource by ID from the config and converts it to a domain.Resource.
func findSSHResource(cfg *config.ConfigV2, id string) (*domain.Resource, error) {
	for _, r := range cfg.Resources {
		if r.ID == id {
			return &domain.Resource{
				ID:        r.ID,
				Name:      r.Name,
				Type:      domain.ResourceType(r.Type),
				Connector: r.Connector,
				Config:    r.Config,
				Tags:      r.Tags,
				DependsOn: r.DependsOn,
			}, nil
		}
	}
	return nil, fmt.Errorf("resource %q not found", id)
}

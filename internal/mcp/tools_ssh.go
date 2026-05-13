package mcp

import (
	"context"
	"fmt"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

// NewCerberusSSHExecTool creates the cerberus_ssh_exec tool.
func NewCerberusSSHExecTool(cfg *config.ConfigV2, client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_ssh_exec",
		Description: "Executes a command on a remote host via SSH. Returns stdout, stderr, and exit code.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id":  map[string]interface{}{"type": "string", "description": "ID of the SSH resource to connect to."},
				"command":      map[string]interface{}{"type": "string", "description": "Command to execute on the remote host."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Set true to preview the remote command without executing it."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Set true to acknowledge this destructive remote execution."},
			},
			"required": []string{"resource_id", "command"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID := stringArg(args, "resource_id")
			command := stringArg(args, "command")
			res, err := findSSHResource(cfg, resourceID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil
			}
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector:    "ssh",
				Operation:    "exec",
				Config:       sshToolConfig(res, command),
				DryRun:       boolArg(args, "dry_run"),
				Acknowledged: boolArg(args, "acknowledged"),
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil
			}
			return marshalConnectorData(result.Data)
		},
	}
}

// NewCerberusSSHStatusTool creates the cerberus_ssh_status tool.
func NewCerberusSSHStatusTool(cfg *config.ConfigV2, client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_ssh_status",
		Description: "Checks connectivity and OS info for a remote host via SSH.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{"type": "string", "description": "ID of the SSH resource to check."},
			},
			"required": []string{"resource_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID := stringArg(args, "resource_id")
			res, err := findSSHResource(cfg, resourceID)
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil
			}
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "ssh",
				Operation: "status",
				Config:    sshToolConfig(res, ""),
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil
			}
			return marshalConnectorData(result.Data)
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

func sshToolConfig(res *domain.Resource, command string) map[string]any {
	cfg := make(map[string]any, len(res.Config)+3)
	for key, value := range res.Config {
		cfg[key] = value
	}
	cfg["id"] = res.ID
	cfg["name"] = res.Name
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}

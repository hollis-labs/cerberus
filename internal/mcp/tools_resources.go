package mcp

import (
	"context"
	"encoding/json"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// NewCerberusProjectListTool creates the cerberus_project_list tool.
func NewCerberusProjectListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_project_list",
		Description: "Lists all projects defined in the Cerberus config with their resource counts.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			list, err := client.ListProjects(context.Background())
			if err != nil {
				return "", err
			}
			if list == nil {
				list = []cerbapi.ProjectInfo{}
			}
			data, err := json.MarshalIndent(list, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusResourceListTool creates the cerberus_resource_list tool.
func NewCerberusResourceListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_list",
		Description: "Lists all resources defined in the Cerberus config. Optionally filter by project_id, connector, or tag. Local process resources include runtime metadata such as mode, supervisor, and run_from.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"project_id": map[string]interface{}{
					"type":        "string",
					"description": "Filter resources by project ID.",
				},
				"connector": map[string]interface{}{
					"type":        "string",
					"description": "Filter resources by connector type (e.g. 'local', 'digitalocean').",
				},
				"tag": map[string]interface{}{
					"type":        "string",
					"description": "Filter resources by tag.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			projectID, _ := args["project_id"].(string)
			connectorFilter, _ := args["connector"].(string)
			tagFilter, _ := args["tag"].(string)

			list, err := client.ListResources(context.Background(), cerbapi.ResourceListArgs{
				ProjectID: projectID,
				Connector: connectorFilter,
				Tag:       tagFilter,
			})
			if err != nil {
				return "", err
			}
			if list == nil {
				list = []cerbapi.ResourceInfo{}
			}
			data, err := json.MarshalIndent(list, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusResourceStatusTool creates the cerberus_resource_status tool.
func NewCerberusResourceStatusTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_status",
		Description: "Returns runtime status for a specific resource. Currently supports local process resources and reports status through their configured runtime backend.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to inspect.",
				},
			},
			"required": []string{"resource_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			st, err := client.GetResourceRuntime(context.Background(), resourceID)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(st, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusResourceApplyTool creates the cerberus_resource_apply tool.
func NewCerberusResourceApplyTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_apply",
		Description: "Applies a specific resource through its configured runtime backend. For local os_service resources on macOS, this syncs the installed artifact and updates the launch agent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to apply.",
				},
			},
			"required": []string{"resource_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.ApplyResource(context.Background(), resourceID)
			if err != nil {
				return "", err
			}
			return marshalResult(lifecycleResult{
				Success: res.Success,
				Message: res.Message,
				Error:   res.Error,
			}), nil
		},
	}
}

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
		Description: "Lists all resources defined in the Cerberus config. Optionally filter by project_id, connector, or tag.",
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

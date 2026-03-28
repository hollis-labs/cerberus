package mcp

import (
	"encoding/json"
	"strings"

	"github.com/chrispian/cerberus/internal/config"
)

// projectEntry is the JSON output for a single project.
type projectEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Resources   int    `json:"resource_count"`
}

// NewCerberusProjectListTool creates the cerberus_project_list tool.
func NewCerberusProjectListTool(cfg *config.ConfigV2) Tool {
	return Tool{
		Name:        "cerberus_project_list",
		Description: "Lists all projects defined in the Cerberus config with their resource counts.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			// Count resources per project
			counts := make(map[string]int)
			for _, r := range cfg.Resources {
				counts[r.Project]++
			}

			entries := make([]projectEntry, 0, len(cfg.Projects))
			for _, p := range cfg.Projects {
				entries = append(entries, projectEntry{
					ID:          p.ID,
					Name:        p.Name,
					Description: p.Description,
					Resources:   counts[p.ID],
				})
			}

			data, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// resourceEntry is the JSON output for a single resource.
type resourceEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Project   string   `json:"project"`
	Connector string   `json:"connector"`
	Tags      []string `json:"tags,omitempty"`
}

// NewCerberusResourceListTool creates the cerberus_resource_list tool.
func NewCerberusResourceListTool(cfg *config.ConfigV2) Tool {
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

			var entries []resourceEntry
			for _, r := range cfg.Resources {
				if projectID != "" && r.Project != projectID {
					continue
				}
				if connectorFilter != "" && r.Connector != connectorFilter {
					continue
				}
				if tagFilter != "" && !containsTag(r.Tags, tagFilter) {
					continue
				}

				entries = append(entries, resourceEntry{
					ID:        r.ID,
					Name:      r.Name,
					Type:      r.Type,
					Project:   r.Project,
					Connector: r.Connector,
					Tags:      r.Tags,
				})
			}

			data, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

func containsTag(tags []string, target string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, target) {
			return true
		}
	}
	return false
}

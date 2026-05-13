package mcp

import (
	"context"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

func NewCerberusServerListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_server_list",
		Description: "List DigitalOcean droplets with status and addressing details.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "digitalocean",
				Operation: "list_droplets",
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalConnectorData(result.Data)
		},
	}
}

func NewCerberusServerShowTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_server_show",
		Description: "Show DigitalOcean droplet details.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"droplet_id": map[string]interface{}{
					"type":        "integer",
					"description": "DigitalOcean droplet ID.",
				},
			},
			"required": []string{"droplet_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			dropletID, ok := args["droplet_id"].(float64)
			if !ok || dropletID <= 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "droplet_id is required"}), nil
			}
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "digitalocean",
				Operation: "get_droplet",
				Config:    map[string]any{"droplet_id": int(dropletID)},
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalConnectorData(result.Data)
		},
	}
}

func NewCerberusServerCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_server_create",
		Description: "Create a DigitalOcean droplet.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":      map[string]interface{}{"type": "string", "description": "Droplet name."},
				"region":    map[string]interface{}{"type": "string", "description": "Region slug."},
				"size":      map[string]interface{}{"type": "string", "description": "Size slug."},
				"image":     map[string]interface{}{"type": "string", "description": "Image slug."},
				"ssh_keys":  map[string]interface{}{"type": "array", "description": "SSH key fingerprints.", "items": map[string]interface{}{"type": "string"}},
				"user_data": map[string]interface{}{"type": "string", "description": "Cloud-init user-data."},
				"dry_run":   map[string]interface{}{"type": "boolean", "description": "Preview only."},
			},
			"required": []string{"name", "region", "size", "image"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			cfg := map[string]any{
				"name":      stringArg(args, "name"),
				"region":    stringArg(args, "region"),
				"size":      stringArg(args, "size"),
				"image":     stringArg(args, "image"),
				"user_data": stringArg(args, "user_data"),
			}
			if keys, ok := args["ssh_keys"].([]interface{}); ok {
				out := make([]string, 0, len(keys))
				for _, key := range keys {
					if text, ok := key.(string); ok && text != "" {
						out = append(out, text)
					}
				}
				cfg["ssh_keys"] = out
			}
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector: "digitalocean",
				Operation: "create_droplet",
				Config:    cfg,
				DryRun:    boolArg(args, "dry_run"),
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalConnectorData(result.Data)
		},
	}
}

func NewCerberusServerStartTool(client cerbapi.Client) Tool {
	return newServerLifecycleTool(client, "cerberus_server_start", "start", "Power on a DigitalOcean droplet.")
}

func NewCerberusServerStopTool(client cerbapi.Client) Tool {
	return newServerLifecycleTool(client, "cerberus_server_stop", "stop", "Power off a DigitalOcean droplet.")
}

func NewCerberusServerDestroyTool(client cerbapi.Client) Tool {
	return newServerLifecycleTool(client, "cerberus_server_destroy", "destroy", "Destroy a DigitalOcean droplet.")
}

func newServerLifecycleTool(client cerbapi.Client, name, operation, description string) Tool {
	return Tool{
		Name:        name,
		Description: description,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"droplet_id":   map[string]interface{}{"type": "integer", "description": "DigitalOcean droplet ID."},
				"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
				"acknowledged": map[string]interface{}{"type": "boolean", "description": "Required for destructive destroy operations."},
			},
			"required": []string{"droplet_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			dropletID, ok := args["droplet_id"].(float64)
			if !ok || dropletID <= 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "droplet_id is required"}), nil
			}
			result, err := client.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{
				Connector:    "digitalocean",
				Operation:    operation,
				Config:       map[string]any{"droplet_id": int(dropletID)},
				DryRun:       boolArg(args, "dry_run"),
				Acknowledged: boolArg(args, "acknowledged"),
			})
			if err != nil {
				return marshalResult(lifecycleResult{Success: false, Error: err.Error()}), nil //nolint:nilerr
			}
			return marshalConnectorData(result.Data)
		},
	}
}

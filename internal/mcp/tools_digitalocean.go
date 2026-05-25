package mcp

import (
	"context"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

func NewCerberusDropletListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_droplet_list",
		Description: "List DigitalOcean droplets.",
		InputSchema: emptyObjectSchema(),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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

func NewCerberusDropletGetTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_droplet_get",
		Description: "Get details for one DigitalOcean droplet.",
		InputSchema: objectSchema(map[string]interface{}{
			"droplet_id": map[string]interface{}{
				"type":        "integer",
				"description": "DigitalOcean droplet ID.",
			},
		}, "droplet_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			dropletID, ok := args["droplet_id"].(float64)
			if !ok || dropletID <= 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "droplet_id is required"}), nil
			}
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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

func NewCerberusDropletCreateTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_droplet_create",
		Description: "Create a DigitalOcean droplet.",
		InputSchema: objectSchema(map[string]interface{}{
			"name":      map[string]interface{}{"type": "string", "description": "Droplet name."},
			"region":    map[string]interface{}{"type": "string", "description": "Region slug."},
			"size":      map[string]interface{}{"type": "string", "description": "Size slug."},
			"image":     map[string]interface{}{"type": "string", "description": "Image slug."},
			"ssh_keys":  map[string]interface{}{"type": "array", "description": "SSH key fingerprints.", "items": map[string]interface{}{"type": "string"}},
			"user_data": map[string]interface{}{"type": "string", "description": "Cloud-init user-data."},
			"dry_run":   map[string]interface{}{"type": "boolean", "description": "Preview only."},
		}, "name", "region", "size", "image"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
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
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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

func NewCerberusDropletStartTool(client cerbapi.Client) Tool {
	return newDropletLifecycleTool(client, "cerberus_droplet_start", "start", "Start a DigitalOcean droplet.")
}

func NewCerberusDropletStopTool(client cerbapi.Client) Tool {
	return newDropletLifecycleTool(client, "cerberus_droplet_stop", "stop", "Stop a DigitalOcean droplet.")
}

func NewCerberusDropletDestroyTool(client cerbapi.Client) Tool {
	return newDropletLifecycleTool(client, "cerberus_droplet_destroy", "destroy", "Destroy a DigitalOcean droplet.")
}

func newDropletLifecycleTool(client cerbapi.Client, name, operation, description string) Tool {
	return Tool{
		Name:        name,
		Description: description,
		InputSchema: objectSchema(map[string]interface{}{
			"droplet_id":   map[string]interface{}{"type": "integer", "description": "DigitalOcean droplet ID."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge destructive destroy operations."},
		}, "droplet_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			dropletID, ok := args["droplet_id"].(float64)
			if !ok || dropletID <= 0 {
				return marshalResult(lifecycleResult{Success: false, Error: "droplet_id is required"}), nil
			}
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
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

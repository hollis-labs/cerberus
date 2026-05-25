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
		Description: "List projects and resource counts.",
		InputSchema: emptyObjectSchema(),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			list, err := client.ListProjects(ctx)
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
		Description: "List resources. Optional filters: project_id, connector, tag.",
		InputSchema: objectSchema(map[string]interface{}{
			"project_id": map[string]interface{}{
				"type":        "string",
				"description": "Project ID filter.",
			},
			"connector": map[string]interface{}{
				"type":        "string",
				"description": "Connector ID filter, such as local or ssh.",
			},
			"tag": map[string]interface{}{
				"type":        "string",
				"description": "Tag filter.",
			},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			projectID, _ := args["project_id"].(string)
			connectorFilter, _ := args["connector"].(string)
			tagFilter, _ := args["tag"].(string)

			list, err := client.ListResources(ctx, cerbapi.ResourceListArgs{
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
		Description: "Get runtime status for one resource. Use before deploy, apply, reload, or remove.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			st, err := client.GetResourceRuntime(ctx, resourceID)
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

// NewCerberusResourceInspectTool creates the cerberus_resource_inspect tool.
func NewCerberusResourceInspectTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_inspect",
		Description: "Get runtime details, install paths, and log paths for one resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			st, err := client.GetResourceInspect(ctx, resourceID)
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

// NewCerberusResourceDoctorTool creates the cerberus_resource_doctor tool.
func NewCerberusResourceDoctorTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_doctor",
		Description: "Run checks for one resource and return pass, warn, or fail guidance.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			st, err := client.GetResourceDoctor(ctx, resourceID)
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

// NewCerberusResourceLogsTool creates the cerberus_resource_logs tool.
func NewCerberusResourceLogsTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_logs",
		Description: "Get recent logs for one resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
			"lines": map[string]interface{}{
				"type":        "integer",
				"description": "Number of lines to return. Default 50.",
			},
			"stream": map[string]interface{}{
				"type":        "string",
				"description": "Log stream.",
				"enum":        []string{"stdout", "stderr"},
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			lines := 50
			if raw, ok := args["lines"].(float64); ok && raw > 0 {
				lines = int(raw)
			}
			stream, _ := args["stream"].(string)
			out, err := client.ResourceLogs(ctx, resourceID, lines, stream)
			if err != nil {
				return "", err
			}
			data, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// NewCerberusResourceReloadTool creates the cerberus_resource_reload tool.
func NewCerberusResourceReloadTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_reload",
		Description: "Restart an installed resource without rebuilding or syncing.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.ReloadResource(ctx, resourceID)
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

// NewCerberusResourceStopTool creates the cerberus_resource_stop tool.
func NewCerberusResourceStopTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_stop",
		Description: "Stop a resource without uninstalling it. Use remove for uninstall.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.StopResource(ctx, resourceID)
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

// NewCerberusResourceDeployTool creates the cerberus_resource_deploy tool.
func NewCerberusResourceDeployTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_deploy",
		Description: "Build, sync, and apply a resource from the current source tree.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.DeployResource(ctx, resourceID)
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

// NewCerberusResourceApplyTool creates the cerberus_resource_apply tool.
func NewCerberusResourceApplyTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_apply",
		Description: "Apply a resource without running its build step.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.ApplyResource(ctx, resourceID)
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

// NewCerberusResourceSyncTool creates the cerberus_resource_sync tool.
func NewCerberusResourceSyncTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_sync",
		Description: "Sync installed artifacts without applying the runtime backend.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.SyncResource(ctx, resourceID)
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

// NewCerberusResourceRemoveTool creates the cerberus_resource_remove tool.
func NewCerberusResourceRemoveTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_remove",
		Description: "Uninstall a resource and remove installed artifacts. Use stop to pause only.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				}), nil
			}
			res, err := client.RemoveResource(ctx, resourceID)
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

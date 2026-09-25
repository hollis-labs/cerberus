package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusProjectListTool creates the cerberus_project_list tool.
func NewCerberusProjectListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_project_list",
		Description: "List projects and resource counts. Returns a budgeted envelope ({items,count,total,truncated,hint}).",
		InputSchema: objectSchema(map[string]interface{}{
			"limit":  limitSchemaProp(),
			"offset": offsetSchemaProp(),
		}),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			list, err := client.ListProjects(ctx)
			if err != nil {
				return "", err
			}
			if list == nil {
				list = []cerbapi.ProjectInfo{}
			}
			return budgetedList("cerberus_project_list", list, args, "%d projects total."), nil
		},
	}
}

// NewCerberusResourceListTool creates the cerberus_resource_list tool.
func NewCerberusResourceListTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_list",
		Description: "List resources. Optional filters: project_id, connector, tag. Returns a budgeted envelope ({items,count,total,truncated,hint}); narrow with filters if truncated.",
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
			"limit":  limitSchemaProp(),
			"offset": offsetSchemaProp(),
		}),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
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
			return budgetedList("cerberus_resource_list", list, args,
				"%d resources match; narrow with project_id, connector, or tag to see the rest."), nil
		},
	}
}

// NewCerberusResourceStatusTool creates the cerberus_resource_status tool.
func NewCerberusResourceStatusTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_status",
		Description: "Get runtime status for one resource. Read artifact_stale and recommended_action / recommended_next_step and act on them — or just call cerberus_resource_ensure_fresh to perform the recommended action automatically. Use before deploy, apply, reload, or remove.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			st, err := client.GetResourceRuntime(ctx, resourceID)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(st, "", "  ")
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
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			st, err := client.GetResourceInspect(ctx, resourceID)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(st, "", "  ")
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
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			st, err := client.GetResourceDoctor(ctx, resourceID)
			if err != nil {
				return "", err
			}
			data, err := redact.MarshalIndent(st, "", "  ")
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
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
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
			data, err := redact.MarshalIndent(out, "", "  ")
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
		Name:            "cerberus_resource_reload",
		Description:     "Restart an installed resource WITHOUT rebuilding or syncing — relaunches the existing (possibly stale) artifact. If the source changed, use cerberus_resource_deploy or cerberus_resource_ensure_fresh with force=true instead.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.ReloadResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(lifecycleResult{
				Success: res.Success,
				Message: res.Message,
				Error:   res.Error,
			})
		},
	}
}

// NewCerberusResourceStopTool creates the cerberus_resource_stop tool.
func NewCerberusResourceStopTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_stop",
		Description:     "Stop a resource without uninstalling it. Use remove for uninstall.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.StopResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(lifecycleResult{
				Success: res.Success,
				Message: res.Message,
				Error:   res.Error,
			})
		},
	}
}

// NewCerberusResourceDeployTool creates the cerberus_resource_deploy tool.
func NewCerberusResourceDeployTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_deploy",
		Description:     "Build, sync, and apply a resource from the current source tree. Use this when source changed and you want the running service to match it — run_from: artifact services run an installed copy, so building (go/make) or reloading alone does NOT update them. This is the default action after editing source. ensure_fresh requires force=true to guarantee a rebuild.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.DeployResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(*res)
		},
	}
}

// NewCerberusResourceEnsureFreshTool creates the cerberus_resource_ensure_fresh tool.
func NewCerberusResourceEnsureFreshTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_ensure_fresh",
		Description:     "Reconcile drift between built binaries and installed/running resources. Source edits are NOT checked. After editing source, use cerberus_resource_deploy or pass force=true to rebuild, install and activate. Without force, this may apply/sync existing binaries or report no built-binary drift. Unconfirmed activation returns success=false without restarting; follow the verification guidance before retrying.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
			"force": map[string]interface{}{
				"type":        "boolean",
				"description": "Always rebuild, install and activate. Required after source edits in ANY runtime mode; default false only checks built-binary drift.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			force, _ := args["force"].(bool)
			res, err := cerbapi.EnsureFresh(ctx, client, resourceID, force)
			if err != nil {
				return "", err
			}
			if !res.Success {
				return nil, toolFailure{message: res.Message, content: res}
			}
			data, mErr := redact.MarshalIndent(res, "", "  ")
			if mErr != nil {
				return "", mErr
			}
			return string(data), nil
		},
	}
}

// NewCerberusResourceApplyTool creates the cerberus_resource_apply tool.
func NewCerberusResourceApplyTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_apply",
		Description:     "Activate an already-built resource WITHOUT running its build step. If the source changed, use cerberus_resource_deploy (or cerberus_resource_ensure_fresh with force=true) so it rebuilds first.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.ApplyResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(*res)
		},
	}
}

// NewCerberusResourceSyncTool creates the cerberus_resource_sync tool.
func NewCerberusResourceSyncTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_sync",
		Description:     "Sync installed artifacts without applying the runtime backend.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.SyncResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(lifecycleResult{
				Success: res.Success,
				Message: res.Message,
				Error:   res.Error,
			})
		},
	}
}

// NewCerberusResourceRemoveTool creates the cerberus_resource_remove tool.
func NewCerberusResourceRemoveTool(client cerbapi.Client) Tool {
	return Tool{
		Name:            "cerberus_resource_remove",
		Description:     "Uninstall a resource and remove installed artifacts. Use stop to pause only.",
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{
				"type":        "string",
				"description": "Resource ID.",
			},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID, _ := args["resource_id"].(string)
			if resourceID == "" {
				return toolResult(lifecycleResult{
					Success: false,
					Error:   "resource_id is required",
				})
			}
			res, err := client.RemoveResource(ctx, resourceID)
			if err != nil {
				return "", err
			}
			return toolResult(lifecycleResult{
				Success: res.Success,
				Message: res.Message,
				Error:   res.Error,
			})
		},
	}
}

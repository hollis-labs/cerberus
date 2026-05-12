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
		Description: "Lists all projects defined in the v2 Cerberus config with their resource counts.",
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
		Description: "Lists all resources defined in the Cerberus v2 resource lane. Optionally filter by project_id, connector, or tag. Local process resources include runtime metadata such as mode, supervisor, run_from, and current backend state.",
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
		Description: "Returns runtime status for a specific v2 resource. Currently supports local process resources and reports backend state plus operator guidance such as artifact drift, recommended action codes, and a prose next step. Use this before choosing deploy, apply, reload, sync, stop, or remove.",
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

// NewCerberusResourceInspectTool creates the cerberus_resource_inspect tool.
func NewCerberusResourceInspectTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_inspect",
		Description: "Returns detailed runtime, install, and log-path inspection data for a v2 local process resource. Use this when apply or deploy fails and you need log paths, plist/install locations, or launchd details.",
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
			st, err := client.GetResourceInspect(context.Background(), resourceID)
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
		Description: "Runs explicit runtime and install checks for a v2 local process resource and returns pass/warn/fail results plus operator guidance about the best next step.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to diagnose.",
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
			st, err := client.GetResourceDoctor(context.Background(), resourceID)
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
		Description: "Returns the last N lines from a v2 local process resource log stream. For os_service resources on macOS, stream may be stdout or stderr.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to inspect.",
				},
				"lines": map[string]interface{}{
					"type":        "integer",
					"description": "How many lines to return. Defaults to 50.",
				},
				"stream": map[string]interface{}{
					"type":        "string",
					"description": "Log stream to read. Supported values are stdout and stderr.",
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
			lines := 50
			if raw, ok := args["lines"].(float64); ok && raw > 0 {
				lines = int(raw)
			}
			stream, _ := args["stream"].(string)
			out, err := client.ResourceLogs(context.Background(), resourceID, lines, stream)
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
		Description: "V2 resource lane only. Restart/kickstart a local process resource that is already installed. Does not rebuild, sync artifacts, or rewrite service definitions.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to reload.",
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
			res, err := client.ReloadResource(context.Background(), resourceID)
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
		Description: "V2 resource lane only. Stops a local process resource without removing install state. For dev_session resources, this suppresses auto-restart until an explicit apply, deploy, or reload. Use remove only for uninstall intent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to stop.",
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
			res, err := client.StopResource(context.Background(), resourceID)
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
		Description: "V2 resource lane only. Runs the resource's declared build contract first, then syncs/applies it through the configured runtime backend. Use this when the operator intent is source-to-runtime: make the running service match the current source tree.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to deploy.",
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
			res, err := client.DeployResource(context.Background(), resourceID)
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
		Description: "V2 resource lane only. Applies a specific resource through its configured runtime backend. For local os_service resources on macOS, this syncs the currently-built artifact and updates the launch agent. It does not run the build command first.",
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

// NewCerberusResourceSyncTool creates the cerberus_resource_sync tool.
func NewCerberusResourceSyncTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_resource_sync",
		Description: "V2 resource lane only. Syncs a specific resource's installed runtime artifacts without applying the runtime backend. Intended for local process resources using run_from=artifact when the artifact should be copied now and activated later.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to sync.",
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
			res, err := client.SyncResource(context.Background(), resourceID)
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
		Description: "Destructively removes a specific resource from its configured runtime backend. For local os_service resources on macOS, this unloads the launch agent and removes installed artifacts. Use cerberus_resource_stop when you only need to stop the process.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"resource_id": map[string]interface{}{
					"type":        "string",
					"description": "The resource ID to remove.",
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
			res, err := client.RemoveResource(context.Background(), resourceID)
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

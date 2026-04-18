package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

// lifecycleResult is the JSON response shape preserved from pre-CERB-2.
// Kept as a local alias over cerbapi.OpResult so JSON output stays byte-
// identical for MCP consumers that pattern-match on field order.
type lifecycleResult = cerbapi.OpResult

// extractAudit reads the audit fields from MCP tool args.
// Returns the audit context and an error string if reason is missing.
func extractAudit(args map[string]interface{}) (cerbapi.AuditContext, string) {
	ac := cerbapi.AuditContext{
		Reason:    strings.TrimSpace(fmt.Sprintf("%v", args["reason"])),
		TaskID:    strings.TrimSpace(fmt.Sprintf("%v", args["task_id"])),
		SessionID: strings.TrimSpace(fmt.Sprintf("%v", args["session_id"])),
	}
	if ac.TaskID == "<nil>" {
		ac.TaskID = ""
	}
	if ac.SessionID == "<nil>" {
		ac.SessionID = ""
	}
	if ac.Reason == "" || ac.Reason == "<nil>" {
		return ac, "reason is required for destructive operations (stop/restart/rebuild). Provide a short explanation of why this service needs to be stopped."
	}
	return ac, ""
}

// auditProperties returns the shared InputSchema properties for audit fields.
func auditProperties() map[string]interface{} {
	return map[string]interface{}{
		"reason": map[string]interface{}{
			"type":        "string",
			"description": "REQUIRED. Why this service is being stopped/restarted/rebuilt. Include context so operators can understand the intent.",
		},
		"task_id": map[string]interface{}{
			"type":        "string",
			"description": "The Volon task ID that initiated this operation (e.g. TASK-20260312-61233).",
		},
		"session_id": map[string]interface{}{
			"type":        "string",
			"description": "The agent session or conversation ID.",
		},
	}
}

func marshalResult(r lifecycleResult) string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// NewCerberusStartTool creates the cerberus_start tool.
func NewCerberusStartTool(client cerbapi.Client) Tool {
	return Tool{
		Name:        "cerberus_start",
		Description: "Start a Cerberus-managed service by ID. Handles lock acquisition and port conflict detection automatically.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to start.",
				},
				"force": map[string]interface{}{
					"type":        "boolean",
					"description": "Force start even if pre-checks warn. Defaults to false.",
					"default":     false,
				},
			},
			"required": []string{"service_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "service_id is required",
				}), nil
			}
			res, err := client.StartService(context.Background(), serviceID)
			if err != nil {
				return "", err
			}
			return marshalResult(*res), nil
		},
	}
}

// NewCerberusStopTool creates the cerberus_stop tool.
func NewCerberusStopTool(client cerbapi.Client) Tool {
	props := map[string]interface{}{
		"service_id": map[string]interface{}{
			"type":        "string",
			"description": "The service ID to stop.",
		},
	}
	for k, v := range auditProperties() {
		props[k] = v
	}

	return Tool{
		Name:        "cerberus_stop",
		Description: "Stop a Cerberus-managed service by ID. Sends SIGTERM and waits briefly before force-killing. Requires a reason explaining why the service is being stopped.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"service_id", "reason"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "service_id is required",
				}), nil
			}
			audit, auditErr := extractAudit(args)
			if auditErr != "" {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     auditErr,
				}), nil
			}
			res, err := client.StopService(context.Background(), serviceID, audit)
			if err != nil {
				return "", err
			}
			return marshalResult(*res), nil
		},
	}
}

// NewCerberusRestartTool creates the cerberus_restart tool.
func NewCerberusRestartTool(client cerbapi.Client) Tool {
	props := map[string]interface{}{
		"service_id": map[string]interface{}{
			"type":        "string",
			"description": "The service ID to restart.",
		},
		"force": map[string]interface{}{
			"type":        "boolean",
			"description": "If true, proceed with start even if stop returns an error (e.g. service already stopped). Defaults to false.",
			"default":     false,
		},
	}
	for k, v := range auditProperties() {
		props[k] = v
	}

	return Tool{
		Name:        "cerberus_restart",
		Description: "Restart a Cerberus-managed service (stop then start) without rebuilding. To rebuild before restarting, use cerberus_rebuild instead. Requires a reason explaining why the service is being restarted.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"service_id", "reason"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "service_id is required",
				}), nil
			}
			audit, auditErr := extractAudit(args)
			if auditErr != "" {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     auditErr,
				}), nil
			}
			force, _ := args["force"].(bool)
			res, err := client.RestartService(context.Background(), serviceID, cerbapi.RestartServiceArgs{
				Audit: audit,
				Force: force,
			})
			if err != nil {
				return "", err
			}
			return marshalResult(*res), nil
		},
	}
}

// NewCerberusRebuildTool creates the cerberus_rebuild tool.
func NewCerberusRebuildTool(client cerbapi.Client) Tool {
	props := map[string]interface{}{
		"service_id": map[string]interface{}{
			"type":        "string",
			"description": "The service ID to rebuild and restart.",
		},
		"force": map[string]interface{}{
			"type":        "boolean",
			"description": "If true, proceed with restart even if build fails. Defaults to false.",
			"default":     false,
		},
	}
	for k, v := range auditProperties() {
		props[k] = v
	}

	return Tool{
		Name:        "cerberus_rebuild",
		Description: "Build then restart a Cerberus-managed service. Runs the build command first; if build succeeds (or force=true), stops and starts the service. Requires a reason explaining why the service is being rebuilt.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": props,
			"required":   []string{"service_id", "reason"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return marshalResult(lifecycleResult{
					Success: false,
					Error:   "service_id is required",
				}), nil
			}
			audit, auditErr := extractAudit(args)
			if auditErr != "" {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     auditErr,
				}), nil
			}
			force, _ := args["force"].(bool)
			res, err := client.RebuildService(context.Background(), serviceID, cerbapi.RebuildServiceArgs{
				Audit: audit,
				Force: force,
			})
			if err != nil {
				return "", err
			}
			return marshalResult(*res), nil
		},
	}
}

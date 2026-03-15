package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/chrispian/cerberus/internal/service"
)

// lifecycleResult is the JSON response for start/stop/restart/rebuild operations.
type lifecycleResult struct {
	Success     bool   `json:"success"`
	ServiceID   string `json:"service_id"`
	Message     string `json:"message,omitempty"`
	BuildOutput string `json:"build_output,omitempty"`
	Error       string `json:"error,omitempty"`
}

// auditContext captures who requested a destructive lifecycle operation and why.
type auditContext struct {
	Reason    string `json:"reason"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// extractAudit reads the audit fields from MCP tool args.
// Returns the audit context and an error string if reason is missing.
func extractAudit(args map[string]interface{}) (auditContext, string) {
	ac := auditContext{
		Reason:    strings.TrimSpace(fmt.Sprintf("%v", args["reason"])),
		TaskID:    strings.TrimSpace(fmt.Sprintf("%v", args["task_id"])),
		SessionID: strings.TrimSpace(fmt.Sprintf("%v", args["session_id"])),
	}
	// Clean up "<nil>" from missing optional fields
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

// logAudit writes an audit entry to the Cerberus lifecycle log.
func logAudit(operation, serviceID string, ac auditContext) {
	service.LogAudit(operation, serviceID, ac.Reason, ac.TaskID, ac.SessionID)
}

func marshalResult(r lifecycleResult) string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// findService looks up a service by ID from the services slice.
func findService(services []*service.ManagedService, id string) *service.ManagedService {
	for _, svc := range services {
		if svc.Def.ID == id {
			return svc
		}
	}
	return nil
}

// NewCerberusStartTool creates the cerberus_start tool.
func NewCerberusStartTool(services []*service.ManagedService) Tool {
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

			svc := findService(services, serviceID)
			if svc == nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", serviceID),
				}), nil
			}

			if err := svc.Start(); err != nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("failed to start service %q: %s", serviceID, err.Error()),
				}), nil
			}

			return marshalResult(lifecycleResult{
				Success:   true,
				ServiceID: serviceID,
				Message:   fmt.Sprintf("service %q started successfully", serviceID),
			}), nil
		},
	}
}

// NewCerberusStopTool creates the cerberus_stop tool.
func NewCerberusStopTool(services []*service.ManagedService) Tool {
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

			svc := findService(services, serviceID)
			if svc == nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", serviceID),
				}), nil
			}

			// Check if service is protected
			if svc.Def.Protected {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", serviceID),
				}), nil
			}

			logAudit("stop", serviceID, audit)

			// Pause auto-restart so the monitor doesn't undo the stop.
			_ = pausectl.PauseService(serviceID)
			// NOTE: no defer resume — a deliberate stop should stay stopped
			// until the user explicitly starts or resumes the service.

			if err := svc.Stop(); err != nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("failed to stop service %q: %s", serviceID, err.Error()),
				}), nil
			}

			return marshalResult(lifecycleResult{
				Success:   true,
				ServiceID: serviceID,
				Message:   fmt.Sprintf("service %q stopped successfully (reason: %s)", serviceID, audit.Reason),
			}), nil
		},
	}
}

// NewCerberusRestartTool creates the cerberus_restart tool.
func NewCerberusRestartTool(services []*service.ManagedService) Tool {
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

			svc := findService(services, serviceID)
			if svc == nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", serviceID),
				}), nil
			}

			// Check if service is protected
			if svc.Def.Protected {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", serviceID),
				}), nil
			}

			logAudit("restart", serviceID, audit)

			// Pause auto-restart so the monitor doesn't race us.
			_ = pausectl.PauseService(serviceID)
			defer func() { _ = pausectl.ResumeService(serviceID) }()

			if err := svc.Stop(); err != nil {
				if !force {
					return marshalResult(lifecycleResult{
						Success:   false,
						ServiceID: serviceID,
						Error:     fmt.Sprintf("failed to stop service %q during restart: %s (use force=true to proceed anyway)", serviceID, err.Error()),
					}), nil
				}
				// force=true: continue to start despite stop error
			}

			if err := svc.Start(); err != nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q stopped but failed to start: %s", serviceID, err.Error()),
				}), nil
			}

			return marshalResult(lifecycleResult{
				Success:   true,
				ServiceID: serviceID,
				Message:   fmt.Sprintf("service %q restarted successfully (reason: %s)", serviceID, audit.Reason),
			}), nil
		},
	}
}

// NewCerberusRebuildTool creates the cerberus_rebuild tool.
func NewCerberusRebuildTool(services []*service.ManagedService) Tool {
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

			svc := findService(services, serviceID)
			if svc == nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", serviceID),
				}), nil
			}

			// Check if service is protected
			if svc.Def.Protected {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q is protected and cannot be stopped/restarted via external tools. Only Cerberus daemon auto-recovery can manage this service.", serviceID),
				}), nil
			}

			logAudit("rebuild", serviceID, audit)

			// Pause auto-restart so the monitor doesn't race us mid-build.
			_ = pausectl.PauseService(serviceID)
			defer func() { _ = pausectl.ResumeService(serviceID) }()

			var buildOutput string
			if len(svc.Def.Build) > 0 {
				out, err := svc.BuildSync()
				buildOutput = strings.TrimSpace(out)
				if err != nil && !force {
					return marshalResult(lifecycleResult{
						Success:     false,
						ServiceID:   serviceID,
						BuildOutput: buildOutput,
						Error:       fmt.Sprintf("build failed for %q: %s (use force=true to restart anyway)", serviceID, err.Error()),
					}), nil
				}
			}

			svc.Stop()
			if err := svc.Start(); err != nil {
				return marshalResult(lifecycleResult{
					Success:     false,
					ServiceID:   serviceID,
					BuildOutput: buildOutput,
					Error:       fmt.Sprintf("build succeeded but failed to start %q: %s", serviceID, err.Error()),
				}), nil
			}

			return marshalResult(lifecycleResult{
				Success:     true,
				ServiceID:   serviceID,
				BuildOutput: buildOutput,
				Message:     fmt.Sprintf("service %q rebuilt and restarted successfully (reason: %s)", serviceID, audit.Reason),
			}), nil
		},
	}
}

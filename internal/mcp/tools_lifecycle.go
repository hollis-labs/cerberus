package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/chrispian/cerberus/internal/service"
)

// lifecycleResult is the JSON response for start/stop/restart operations.
type lifecycleResult struct {
	Success   bool   `json:"success"`
	ServiceID string `json:"service_id"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

func marshalResult(r lifecycleResult) string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// findService looks up a service by ID from the services slice.
func findService(services []*service.Service, id string) *service.Service {
	for _, svc := range services {
		if svc.Def.ID == id {
			return svc
		}
	}
	return nil
}

// NewCerberusStartTool creates the cerberus_start tool.
func NewCerberusStartTool(services []*service.Service) Tool {
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
func NewCerberusStopTool(services []*service.Service) Tool {
	return Tool{
		Name:        "cerberus_stop",
		Description: "Stop a Cerberus-managed service by ID. Sends SIGTERM and waits briefly before force-killing.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to stop.",
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
				Message:   fmt.Sprintf("service %q stopped successfully", serviceID),
			}), nil
		},
	}
}

// NewCerberusRestartTool creates the cerberus_restart tool.
func NewCerberusRestartTool(services []*service.Service) Tool {
	return Tool{
		Name:        "cerberus_restart",
		Description: "Restart a Cerberus-managed service (stop then start). Use force=true to proceed with start even if stop fails.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to restart.",
				},
				"force": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, proceed with start even if stop returns an error (e.g. service already stopped). Defaults to false.",
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

			force, _ := args["force"].(bool)

			svc := findService(services, serviceID)
			if svc == nil {
				return marshalResult(lifecycleResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     fmt.Sprintf("service %q not found; check service_id against cerberus_status output", serviceID),
				}), nil
			}

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
				Message:   fmt.Sprintf("service %q restarted successfully", serviceID),
			}), nil
		},
	}
}

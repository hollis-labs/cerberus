package mcp

import (
	"encoding/json"
	"time"

	"github.com/chrispian/cerberus/internal/service"
)

// serviceStatusEntry is the JSON output for a single service's status.
type serviceStatusEntry struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	PID           int     `json:"pid"`
	Port          int     `json:"port"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Health        string  `json:"health"`
	Error         string  `json:"error"`
}

// NewCerberusStatusTool creates the cerberus_status tool.
// The services slice is polled each time the tool is called.
func NewCerberusStatusTool(services []*service.Service) Tool {
	return Tool{
		Name:        "cerberus_status",
		Description: "Returns the current status of Cerberus-managed services. Optionally filter by service_id.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional service ID to filter. If omitted, returns all services.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			filterID, _ := args["service_id"].(string)

			var entries []serviceStatusEntry
			for _, svc := range services {
				if filterID != "" && svc.Def.ID != filterID {
					continue
				}

				svc.Poll()

				var uptimeSeconds float64
				if svc.Status != service.StatusStopped && !svc.Uptime.IsZero() {
					uptimeSeconds = time.Since(svc.Uptime).Seconds()
				}

				health := ""
				if svc.HealthStatus.LastCheck.IsZero() {
					health = "unknown"
				} else if svc.HealthStatus.Healthy {
					health = "healthy"
				} else {
					health = "unhealthy"
				}

				entries = append(entries, serviceStatusEntry{
					ID:            svc.Def.ID,
					Name:          svc.Def.Name,
					Status:        svc.Status.String(),
					PID:           svc.PID,
					Port:          svc.Def.Port,
					UptimeSeconds: uptimeSeconds,
					Health:        health,
					Error:         svc.Error,
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

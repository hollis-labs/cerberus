package mcp

import (
	"encoding/json"
	"time"

	"github.com/chrispian/cerberus/internal/daemon"
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
	Protected     bool    `json:"protected"`
	AutoRestart   bool    `json:"auto_restart"`
	RestartCount  int     `json:"restart_count,omitempty"`
	LastRestartAt string  `json:"last_restart_at,omitempty"`
	DaemonState   string  `json:"daemon_state,omitempty"`
	// Stale indicates the on-disk config for this service changed since
	// the running process was started. Clears on the next successful
	// Start() (or Rebuild/Restart).
	Stale bool `json:"stale,omitempty"`
}

// NewCerberusStatusTool creates the cerberus_status tool.
// The registry is reloaded from disk on each invocation so the status
// reflects the current on-disk config (including services added/removed
// since the daemon started). If monitor is non-nil, daemon restart stats
// are included per service.
func NewCerberusStatusTool(reg *service.ServiceRegistry, monitor *daemon.Monitor) Tool {
	return Tool{
		Name:        "cerberus_status",
		Description: "Returns the current status of Cerberus-managed services. Optionally filter by service_id. Includes daemon protection and auto-restart state.",
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

			// Status reads live from disk so it sees services that were
			// added/removed after the daemon started. Reload failures are
			// logged by the registry; we proceed with the last-good list.
			_ = reg.Reload()

			// Grab monitor status once if available
			var monStatus *daemon.MonitorStatus
			if monitor != nil {
				ms := monitor.GetStatus()
				monStatus = &ms
			}

			var entries []serviceStatusEntry
			for _, svc := range reg.Current() {
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

				entry := serviceStatusEntry{
					ID:            svc.Def.ID,
					Name:          svc.Def.Name,
					Status:        svc.Status.String(),
					PID:           svc.PID,
					Port:          svc.Def.Port,
					UptimeSeconds: uptimeSeconds,
					Health:        health,
					Error:         svc.Error,
					Protected:     svc.Def.Protected,
					AutoRestart:   svc.Def.AutoRestart,
					Stale:         svc.Stale,
				}

				// Add daemon monitor stats if available
				if monStatus != nil {
					if stats, ok := monStatus.ServiceStats[svc.Def.ID]; ok {
						entry.RestartCount = stats.FailureCount
						if stats.LastRestart != nil {
							entry.LastRestartAt = stats.LastRestart.Format(time.RFC3339)
						}
					}

					// Derive daemon_state
					maxAttempts := 3 // default
					if svc.Def.MaxRestartAttempts > 0 {
						maxAttempts = svc.Def.MaxRestartAttempts
					}
					entry.DaemonState = deriveDaemonState(svc, monStatus, maxAttempts)
				}

				entries = append(entries, entry)
			}

			data, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// deriveDaemonState computes a human-readable daemon state for a service.
func deriveDaemonState(svc *service.ManagedService, monStatus *daemon.MonitorStatus, maxAttempts int) string {
	if !svc.Def.AutoRestart && !svc.Def.Protected {
		return "unmanaged"
	}

	stats, hasStats := monStatus.ServiceStats[svc.Def.ID]

	switch svc.Status {
	case service.StatusRunning:
		return "healthy"
	case service.StatusStopped:
		if hasStats && stats.FailureCount >= maxAttempts {
			return "failed"
		}
		return "stopped"
	case service.StatusFailed:
		if hasStats && stats.FailureCount >= maxAttempts {
			return "failed"
		}
		if hasStats && stats.FailureCount > 0 {
			return "restarting"
		}
		return "failed"
	default:
		return "unknown"
	}
}

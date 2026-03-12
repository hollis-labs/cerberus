package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/service"
)

// readLastNLines reads the last n lines from a file efficiently by seeking
// from the end of the file rather than loading the entire file into memory.
func readLastNLines(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := stat.Size()
	if size == 0 {
		return "", nil
	}

	// Read from the end in chunks to find the last N newlines.
	const chunkSize = 4096
	buf := make([]byte, 0, chunkSize)
	newlines := 0
	offset := size

	for offset > 0 && newlines <= n {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		_, err := f.ReadAt(chunk, offset)
		if err != nil && err != io.EOF {
			return "", err
		}

		// Prepend chunk to buf
		buf = append(chunk, buf...)

		// Count newlines in this chunk
		for _, b := range chunk {
			if b == '\n' {
				newlines++
			}
		}
	}

	// Now extract the last N lines from buf
	lines := make([]byte, 0, len(buf))
	found := 0
	// Walk backward through buf to find the Nth newline from the end
	end := len(buf)
	// Trim trailing newline if present
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	start := end
	for start > 0 && found < n {
		start--
		if buf[start] == '\n' {
			found++
			if found == n {
				start++ // move past the newline
				break
			}
		}
	}
	lines = buf[start:end]

	return string(lines), nil
}

// NewCerberusLogsTool creates the cerberus_logs tool.
func NewCerberusLogsTool(services []*service.Service) Tool {
	return Tool{
		Name:        "cerberus_logs",
		Description: "Returns the last N lines from a service's log file.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to read logs for.",
				},
				"lines": map[string]interface{}{
					"type":        "integer",
					"description": "Number of lines to return (default 50).",
				},
			},
			"required": []string{"service_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return "", fmt.Errorf("service_id is required")
			}

			// Find the service
			var svc *service.Service
			for _, s := range services {
				if s.Def.ID == serviceID {
					svc = s
					break
				}
			}
			if svc == nil {
				return "", fmt.Errorf("unknown service: %s", serviceID)
			}

			lines := 50
			if l, ok := args["lines"].(float64); ok && l > 0 {
				lines = int(l)
			}

			logPath := svc.LogPath()
			if logPath == "" {
				return "No log path configured for this service.", nil
			}

			content, err := readLastNLines(logPath, lines)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("Log file does not exist: %s", logPath), nil
				}
				return "", fmt.Errorf("failed to read log file: %w", err)
			}

			if content == "" {
				return fmt.Sprintf("Log file is empty: %s", logPath), nil
			}

			return content, nil
		},
	}
}

// buildResult is the JSON response for cerberus_build.
type buildResult struct {
	Success   bool   `json:"success"`
	ServiceID string `json:"service_id"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

// NewCerberusBuildTool creates the cerberus_build tool.
func NewCerberusBuildTool(services []*service.Service) Tool {
	return Tool{
		Name:        "cerberus_build",
		Description: "Runs the build command for a service synchronously and returns the result.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "The service ID to build.",
				},
			},
			"required": []string{"service_id"},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			serviceID, _ := args["service_id"].(string)
			if serviceID == "" {
				return "", fmt.Errorf("service_id is required")
			}

			var svc *service.Service
			for _, s := range services {
				if s.Def.ID == serviceID {
					svc = s
					break
				}
			}
			if svc == nil {
				return "", fmt.Errorf("unknown service: %s", serviceID)
			}

			if len(svc.Def.Build) == 0 {
				result := buildResult{
					Success:   false,
					ServiceID: serviceID,
					Error:     "no build command configured for this service",
				}
				data, _ := json.MarshalIndent(result, "", "  ")
				return string(data), nil
			}

			output, err := svc.BuildSync()
			if err != nil {
				result := buildResult{
					Success:   false,
					ServiceID: serviceID,
					Output:    output,
					Error:     err.Error(),
				}
				data, _ := json.MarshalIndent(result, "", "  ")
				return string(data), nil
			}

			result := buildResult{
				Success:   true,
				ServiceID: serviceID,
				Output:    output,
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return string(data), nil
		},
	}
}

// healthEntry is the JSON response for a single service's health status.
type healthEntry struct {
	ServiceID           string `json:"service_id"`
	HealthConfigured    bool   `json:"health_configured"`
	Healthy             bool   `json:"healthy"`
	LastCheck           string `json:"last_check,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

// healthResponse is the full JSON response including daemon-level status.
type healthResponse struct {
	Services         []healthEntry `json:"services"`
	DaemonRunning    bool          `json:"daemon_running"`
	MonitorInterval  string        `json:"monitor_interval,omitempty"`
	ServicesProtected int          `json:"services_protected"`
	ServicesFailed   int          `json:"services_failed"`
}

// NewCerberusHealthTool creates the cerberus_health tool.
// If monitor is non-nil, daemon-level health statistics are included.
func NewCerberusHealthTool(services []*service.Service, monitor *daemon.Monitor) Tool {
	return Tool{
		Name:        "cerberus_health",
		Description: "Returns health check results for one or all services, plus daemon monitor status.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional service ID. If omitted, returns health for all services.",
				},
			},
		},
		Handler: func(args map[string]interface{}) (string, error) {
			filterID, _ := args["service_id"].(string)

			var entries []healthEntry
			for _, svc := range services {
				if filterID != "" && svc.Def.ID != filterID {
					continue
				}

				hasHealthCheck := svc.Def.HealthCheckCfg.URL != "" || len(svc.Def.HealthCheckCfg.Command) > 0

				entry := healthEntry{
					ServiceID:        svc.Def.ID,
					HealthConfigured: hasHealthCheck,
				}

				if !hasHealthCheck {
					entries = append(entries, entry)
					continue
				}

				entry.Healthy = svc.HealthStatus.Healthy
				entry.ConsecutiveFailures = svc.HealthStatus.ConsecutiveFailures
				entry.LastError = svc.HealthStatus.LastError

				if !svc.HealthStatus.LastCheck.IsZero() {
					entry.LastCheck = svc.HealthStatus.LastCheck.Format(time.RFC3339)
				}

				entries = append(entries, entry)
			}

			if filterID != "" && len(entries) == 0 {
				return "", fmt.Errorf("unknown service: %s", filterID)
			}

			// Build response with daemon-level fields
			response := healthResponse{
				Services: entries,
			}

			// Add daemon status if monitor is available
			if monitor != nil {
				monStatus := monitor.GetStatus()
				response.DaemonRunning = monStatus.Running
				response.MonitorInterval = monStatus.CheckInterval.String()

				// Count protected services and failed services
				protectedCount := 0
				failedCount := 0
				for _, svc := range services {
					if svc.Def.Protected {
						protectedCount++
					}
					if stats, hasStats := monStatus.ServiceStats[svc.Def.ID]; hasStats {
						// Determine max attempts for this service
						maxAttempts := 3 // default
						if svc.Def.MaxRestartAttempts > 0 {
							maxAttempts = svc.Def.MaxRestartAttempts
						}
						if stats.FailureCount >= maxAttempts {
							failedCount++
						}
					}
				}
				response.ServicesProtected = protectedCount
				response.ServicesFailed = failedCount
			}

			data, err := json.MarshalIndent(response, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

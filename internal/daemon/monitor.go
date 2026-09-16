package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/service"
)

// MonitorConfig holds configuration for the daemon monitor.
type MonitorConfig struct {
	// CheckInterval is how often to check service health (default 30s)
	CheckInterval time.Duration
	// DefaultMaxRestartAttempts is used when service config doesn't specify (default 3)
	DefaultMaxRestartAttempts int
	// DefaultRestartCooldown is used when service config doesn't specify (default 10s)
	DefaultRestartCooldown time.Duration
}

// DefaultMonitorConfig returns sensible defaults for the monitor.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		CheckInterval:             30 * time.Second,
		DefaultMaxRestartAttempts: 3,
		DefaultRestartCooldown:    10 * time.Second,
	}
}

// Monitor periodically checks service health and auto-restarts failed services.
type Monitor struct {
	config   MonitorConfig
	registry *service.ServiceRegistry
	logger   *slog.Logger

	mu      sync.RWMutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	// Track restart failures per service
	failureCount    map[string]int
	lastRestart     map[string]time.Time
	lastError       map[string]string
	allAttempts     map[string][]attemptRecord
	maxExceededOnce map[string]bool // log max_restarts_exceeded only once per service
}

// attemptRecord captures one restart attempt for alert payloads.
type attemptRecord struct {
	Attempt   int       `json:"attempt"`
	Timestamp time.Time `json:"timestamp"`
	Error     string    `json:"error,omitempty"`
	Success   bool      `json:"success"`
}

// NewMonitor creates a new daemon monitor backed by a ServiceRegistry.
//
// The monitor always walks registry.Current() so that services added by a
// config reload are picked up automatically and services removed by a reload
// stop being polled.
func NewMonitor(registry *service.ServiceRegistry, config MonitorConfig) *Monitor {
	return &Monitor{
		config:          config,
		registry:        registry,
		logger:          service.GetLogger(),
		failureCount:    make(map[string]int),
		lastRestart:     make(map[string]time.Time),
		lastError:       make(map[string]string),
		allAttempts:     make(map[string][]attemptRecord),
		maxExceededOnce: make(map[string]bool),
		done:            make(chan struct{}),
	}
}

// Run starts the monitor loop. This blocks until the context is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil // already running
	}
	m.running = true

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		close(m.done)
	}()

	m.logger.Info("daemon.monitor.start", "interval", m.config.CheckInterval.String())

	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	// Run initial check immediately
	m.checkAllServices(ctx)

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("daemon.monitor.stop")
			return ctx.Err()
		case <-ticker.C:
			m.checkAllServices(ctx)
		}
	}
}

// Stop gracefully stops the monitor.
func (m *Monitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}

	if m.cancel != nil {
		m.cancel()
	}

	// Wait for monitor to stop
	<-m.done
}

// IsRunning returns true if the monitor is currently running.
func (m *Monitor) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// GetStatus returns the current daemon monitor status.
func (m *Monitor) GetStatus() MonitorStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := MonitorStatus{
		Running:       m.running,
		CheckInterval: m.config.CheckInterval,
		ServiceStats:  make(map[string]ServiceStats),
	}

	for serviceID, count := range m.failureCount {
		stats := ServiceStats{
			FailureCount: count,
		}
		if lastRestart, exists := m.lastRestart[serviceID]; exists {
			stats.LastRestart = &lastRestart
		}
		status.ServiceStats[serviceID] = stats
	}

	return status
}

// MonitorStatus represents the current state of the daemon monitor.
type MonitorStatus struct {
	Running       bool                    `json:"running"`
	CheckInterval time.Duration           `json:"check_interval"`
	ServiceStats  map[string]ServiceStats `json:"service_stats"`
}

// ServiceStats holds monitoring statistics for a single service.
type ServiceStats struct {
	FailureCount int        `json:"failure_count"`
	LastRestart  *time.Time `json:"last_restart,omitempty"`
}

// checkAllServices checks each service that has auto-restart enabled.
//
// The service list is resolved from the registry on every tick so that
// add/remove events from config reloads take effect without a daemon
// restart.
func (m *Monitor) checkAllServices(ctx context.Context) {
	for _, svc := range m.registry.Current() {
		select {
		case <-ctx.Done():
			return
		default:
			m.checkService(ctx, svc)
		}
	}
}

// checkService checks a single service and restarts it if needed.
func (m *Monitor) checkService(ctx context.Context, svc *service.ManagedService) {
	// Only auto-restart services that explicitly opt in.
	// Protected means "shield from external stop via MCP" — not "auto-restart."
	if !svc.Def.AutoRestart {
		return
	}

	// Poll to get current status
	svc.Poll()

	// Check if service is down
	isDown := svc.Status == service.StatusStopped || svc.Status == service.StatusFailed

	// If service has a port configured, also check port connectivity
	if svc.Def.Port > 0 && !isDown {
		if !m.isPortAlive(svc.Def.Port) {
			isDown = true
			m.logger.Info("daemon.monitor.port_check_failed", "service", svc.Def.ID, "port", svc.Def.Port)
		}
	}

	if !isDown {
		// Service is healthy, reset failure count
		if m.failureCount[svc.Def.ID] > 0 {
			m.logger.Info("daemon.monitor.service_recovered", "service", svc.Def.ID)
			m.failureCount[svc.Def.ID] = 0
			m.lastError[svc.Def.ID] = ""
			m.allAttempts[svc.Def.ID] = nil
			m.maxExceededOnce[svc.Def.ID] = false
		}
		return
	}

	// Check if auto-restart is paused (globally or per-service)
	if pausectl.IsServicePaused(svc.Def.ID) {
		m.logger.Info("daemon.monitor.paused", "service", svc.Def.ID,
			"message", "Auto-restart paused, skipping restart")
		return
	}

	m.logger.Info("daemon.monitor.service_down", "service", svc.Def.ID, "status", svc.Status.String())

	// Check if we've exceeded max restart attempts
	maxAttempts := m.config.DefaultMaxRestartAttempts
	if svc.Def.MaxRestartAttempts > 0 {
		maxAttempts = svc.Def.MaxRestartAttempts
	}

	if m.failureCount[svc.Def.ID] >= maxAttempts {
		// Only log max_restarts_exceeded once per service to avoid log spam
		if !m.maxExceededOnce[svc.Def.ID] {
			m.logger.Warn("daemon.monitor.max_restarts_exceeded",
				"service_id", svc.Def.ID,
				"failure_count", m.failureCount[svc.Def.ID],
				"last_error", m.lastError[svc.Def.ID],
				"max", maxAttempts,
				"message", fmt.Sprintf("Service %s failed %d restart attempts and will not be retried", svc.Def.ID, m.failureCount[svc.Def.ID]))
			m.maxExceededOnce[svc.Def.ID] = true
		}
		return
	}

	// Check restart cooldown
	cooldown := m.config.DefaultRestartCooldown
	if svc.Def.RestartCooldown != "" {
		if d, err := time.ParseDuration(svc.Def.RestartCooldown); err == nil && d > 0 {
			cooldown = d
		}
	}

	if lastRestart, exists := m.lastRestart[svc.Def.ID]; exists {
		if time.Since(lastRestart) < cooldown {
			m.logger.Info("daemon.monitor.cooldown_wait",
				"service", svc.Def.ID,
				"remaining", cooldown-time.Since(lastRestart))
			return
		}
	}

	// Attempt restart
	m.failureCount[svc.Def.ID]++
	m.lastRestart[svc.Def.ID] = time.Now()

	m.logger.Info("daemon.monitor.restart_attempt",
		"service", svc.Def.ID,
		"attempt", m.failureCount[svc.Def.ID],
		"max", maxAttempts)

	if err := svc.Start(); err != nil {
		m.lastError[svc.Def.ID] = err.Error()
		m.allAttempts[svc.Def.ID] = append(m.allAttempts[svc.Def.ID], attemptRecord{
			Attempt:   m.failureCount[svc.Def.ID],
			Timestamp: time.Now(),
			Error:     err.Error(),
			Success:   false,
		})

		m.logger.Error("daemon.monitor.restart_failed",
			"service_id", svc.Def.ID,
			"error", err.Error(),
			"attempt", m.failureCount[svc.Def.ID])

		// If we've hit max attempts, fire alert
		if m.failureCount[svc.Def.ID] >= maxAttempts {
			// Structured WARN log with all required fields
			m.logger.Warn("daemon.monitor.max_restart_attempts_exceeded",
				"service_id", svc.Def.ID,
				"failure_count", m.failureCount[svc.Def.ID],
				"last_error", err.Error(),
				"timestamp", time.Now().Format(time.RFC3339),
				"all_restart_attempt_timestamps", m.allAttempts[svc.Def.ID])

			m.logger.Error("daemon.monitor.service_failed",
				"service_id", svc.Def.ID,
				"failure_count", m.failureCount[svc.Def.ID],
				"last_error", err.Error(),
				"message", fmt.Sprintf("Service %s exceeded %d max restart attempts and will not be retried automatically", svc.Def.ID, maxAttempts))
			m.alert(svc.Def.ID, m.failureCount[svc.Def.ID], err.Error())
		}
	} else {
		m.allAttempts[svc.Def.ID] = append(m.allAttempts[svc.Def.ID], attemptRecord{
			Attempt:   m.failureCount[svc.Def.ID],
			Timestamp: time.Now(),
			Success:   true,
		})
		m.logger.Info("daemon.monitor.restart_success",
			"service", svc.Def.ID,
			"attempt", m.failureCount[svc.Def.ID])
	}
}

// alert handles all three layers of alerting when a service exceeds max restart attempts:
// 1. Structured log (already emitted by caller)
// 2. Volon API notification
// 3. Fallback alert file
func (m *Monitor) alert(serviceID string, failureCount int, lastError string) {
	now := time.Now()

	// Layer 2: Volon notification
	volonOK := m.sendVolonAlert(serviceID, failureCount, lastError)

	// Layer 3: Fallback alert file (always written if Volon failed or was skipped)
	if !volonOK {
		m.writeAlertFile(serviceID, failureCount, lastError, now)
	}
}

// sendVolonAlert attempts to POST a notification to the Volon API.
// Returns true if the POST succeeded (2xx), false otherwise.
func (m *Monitor) sendVolonAlert(serviceID string, failureCount int, lastError string) bool {
	payload := map[string]interface{}{
		"task_id":  "",
		"kind":     "cerberus-alert",
		"severity": "critical",
		"message":  fmt.Sprintf("Service %s failed %d restart attempts. Last error: %s", serviceID, failureCount, lastError),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		m.logger.Warn("daemon.monitor.alert.volon_marshal_failed", "error", err.Error())
		return false
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://127.0.0.1:8085/v1/notifications", "application/json", bytes.NewReader(body))
	if err != nil {
		m.logger.Warn("daemon.monitor.alert.volon_failed", "service_id", serviceID, "error", err.Error())
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		m.logger.Info("daemon.monitor.alert.volon_sent", "service_id", serviceID, "status", resp.StatusCode)
		return true
	}

	m.logger.Warn("daemon.monitor.alert.volon_rejected", "service_id", serviceID, "status", resp.StatusCode)
	return false
}

// writeAlertFile writes a JSON alert to ~/.cerberus/alerts/.
func (m *Monitor) writeAlertFile(serviceID string, failureCount int, lastError string, ts time.Time) {
	home, err := os.UserHomeDir()
	if err != nil {
		m.logger.Warn("daemon.monitor.alert.file_failed", "error", "cannot determine home directory")
		return
	}

	alertDir := filepath.Join(home, ".cerberus", "alerts")
	if err := os.MkdirAll(alertDir, 0755); err != nil {
		m.logger.Warn("daemon.monitor.alert.file_failed", "error", err.Error())
		return
	}

	alert := map[string]interface{}{
		"service_id":    serviceID,
		"failure_count": failureCount,
		"last_error":    lastError,
		"timestamp":     ts.Format(time.RFC3339),
		"all_attempts":  m.allAttempts[serviceID],
	}

	data, err := json.MarshalIndent(alert, "", "  ")
	if err != nil {
		m.logger.Warn("daemon.monitor.alert.file_failed", "error", err.Error())
		return
	}

	filename := fmt.Sprintf("%s-%s.json", serviceID, ts.Format(time.RFC3339))
	alertPath := filepath.Join(alertDir, filename)

	if err := os.WriteFile(alertPath, data, 0644); err != nil {
		m.logger.Warn("daemon.monitor.alert.file_failed", "error", err.Error())
		return
	}

	m.logger.Info("daemon.monitor.alert.file_written", "service_id", serviceID, "path", alertPath)
}

// isPortAlive checks if a port is responding to connections.
// Uses "localhost" so the OS resolver handles IPv4/IPv6 naturally.
// Vite dev servers on macOS bind to ::1 (IPv6 only), so hardcoding
// 127.0.0.1 would cause false-negative port checks.
func (m *Monitor) isPortAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

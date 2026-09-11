package cerbapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pausectl"
)

type ResourceMonitorConfig struct {
	CheckInterval             time.Duration
	DefaultMaxRestartAttempts int
	DefaultRestartCooldown    time.Duration
}

func DefaultResourceMonitorConfig() ResourceMonitorConfig {
	return ResourceMonitorConfig{
		CheckInterval:             30 * time.Second,
		DefaultMaxRestartAttempts: 3,
		DefaultRestartCooldown:    10 * time.Second,
	}
}

type ResourceMonitor struct {
	runtime *ResourceRuntimeService
	config  ResourceMonitorConfig
	logger  *slog.Logger

	mu      sync.RWMutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	failureCount map[string]int
	lastRestart  map[string]time.Time
	lastError    map[string]string
}

func NewResourceMonitor(runtime *ResourceRuntimeService, cfg ResourceMonitorConfig, logger *slog.Logger) *ResourceMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.CheckInterval <= 0 {
		cfg = DefaultResourceMonitorConfig()
	}
	return &ResourceMonitor{
		runtime:      runtime,
		config:       cfg,
		logger:       logger,
		done:         make(chan struct{}),
		failureCount: make(map[string]int),
		lastRestart:  make(map[string]time.Time),
		lastError:    make(map[string]string),
	}
}

func (m *ResourceMonitor) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
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

	m.logger.Info("daemon.resource_monitor.start", "interval", m.config.CheckInterval.String())

	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	m.checkAllResources(ctx)

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("daemon.resource_monitor.stop")
			return ctx.Err()
		case <-ticker.C:
			m.checkAllResources(ctx)
		}
	}
}

func (m *ResourceMonitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	<-done
}

func (m *ResourceMonitor) checkAllResources(ctx context.Context) {
	cfg := m.runtime.snapshotConfig()
	if cfg == nil {
		return
	}
	for i := range cfg.Resources {
		select {
		case <-ctx.Done():
			return
		default:
		}
		res := cfg.Resources[i]
		spec, err := localconn.SpecFromResourceConfig(res.Config)
		if err != nil {
			continue
		}
		if !shouldMonitorResource(res, spec) {
			continue
		}
		m.checkResource(ctx, res, spec)
	}
}

func shouldMonitorResource(res config.ResourceDef, spec localconn.ProcessSpec) bool {
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return false
	}
	mode := spec.Mode
	if mode == "" {
		mode = localconn.ProcessModeDevSession
	}
	return mode == localconn.ProcessModeDevSession && spec.AutoRestart
}

func (m *ResourceMonitor) checkResource(ctx context.Context, res config.ResourceDef, spec localconn.ProcessSpec) {
	if pausectl.IsServicePaused(res.ID) {
		m.logger.Info("daemon.resource_monitor.paused", "resource", res.ID)
		return
	}

	dr := resourceDefToDomain(&res)

	m.runtime.opMu.Lock()
	state, err := m.runtime.localConnector().Status(ctx, dr)
	m.runtime.opMu.Unlock()
	if err != nil {
		m.lastError[res.ID] = err.Error()
		return
	}

	if !resourceStateDown(state) {
		if m.failureCount[res.ID] > 0 {
			m.logger.Info("daemon.resource_monitor.resource_recovered", "resource", res.ID)
			m.failureCount[res.ID] = 0
			m.lastError[res.ID] = ""
		}
		return
	}

	if conflictErr := m.runtime.refusePortConflict(res.ID); conflictErr != nil {
		m.lastError[res.ID] = conflictErr.Error()
		m.logger.Warn("daemon.resource_monitor.port_conflict", "resource", res.ID, "error", conflictErr.Error())
		return
	}
	maxAttempts := m.config.DefaultMaxRestartAttempts
	if spec.MaxRestartAttempts > 0 {
		maxAttempts = spec.MaxRestartAttempts
	}
	if m.failureCount[res.ID] >= maxAttempts {
		m.logger.Warn("daemon.resource_monitor.max_restarts_exceeded",
			"resource", res.ID,
			"failure_count", m.failureCount[res.ID],
			"last_error", m.lastError[res.ID],
			"max", maxAttempts)
		return
	}

	cooldown := m.config.DefaultRestartCooldown
	if spec.RestartCooldown != "" {
		if d, parseErr := time.ParseDuration(spec.RestartCooldown); parseErr == nil && d > 0 {
			cooldown = d
		}
	}
	if last, ok := m.lastRestart[res.ID]; ok && time.Since(last) < cooldown {
		return
	}

	m.failureCount[res.ID]++
	m.lastRestart[res.ID] = time.Now()
	m.logger.Info("daemon.resource_monitor.restart_attempt",
		"resource", res.ID,
		"attempt", m.failureCount[res.ID],
		"max", maxAttempts)

	m.runtime.opMu.Lock()
	_, err = m.runtime.localConnector().Apply(ctx, dr)
	m.runtime.opMu.Unlock()
	if err != nil {
		m.lastError[res.ID] = err.Error()
		m.logger.Warn("daemon.resource_monitor.restart_failed",
			"resource", res.ID,
			"attempt", m.failureCount[res.ID],
			"error", err.Error())
		return
	}

	m.lastError[res.ID] = ""
	m.logger.Info("daemon.resource_monitor.restart_success",
		"resource", res.ID,
		"attempt", m.failureCount[res.ID])
}

func resourceStateDown(state domain.State) bool {
	switch state {
	case domain.StateStopped, domain.StateFailed, domain.StateUnknown:
		return true
	default:
		return false
	}
}

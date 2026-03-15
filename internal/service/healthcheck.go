package service

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultInterval    = 30 * time.Second
	defaultTimeout     = 5 * time.Second
	unhealthyThreshold = 3
)

// HealthStatus represents the current health state of a service.
type HealthStatus struct {
	Healthy             bool
	LastCheck           time.Time
	LastError           string
	ConsecutiveFailures int
}

// HealthChecker runs periodic health checks against a service.
type HealthChecker struct {
	svc      *ManagedService
	interval time.Duration
	timeout  time.Duration

	mu     sync.RWMutex
	status HealthStatus
	cancel context.CancelFunc
	done   chan struct{}
}

// NewHealthChecker creates a HealthChecker for the given service.
// Returns nil if the service has no health check configured.
func NewHealthChecker(svc *ManagedService) *HealthChecker {
	cfg := svc.Def.HealthCheckCfg
	if cfg.URL == "" && len(cfg.Command) == 0 {
		return nil
	}

	interval := defaultInterval
	if cfg.Interval != "" {
		if d, err := time.ParseDuration(cfg.Interval); err == nil && d > 0 {
			interval = d
		}
	}

	timeout := defaultTimeout
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}

	return &HealthChecker{
		svc:      svc,
		interval: interval,
		timeout:  timeout,
		status:   HealthStatus{Healthy: true},
	}
}

// Start begins the background health check loop.
func (hc *HealthChecker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	hc.cancel = cancel
	hc.done = make(chan struct{})

	go func() {
		defer close(hc.done)
		ticker := time.NewTicker(hc.interval)
		defer ticker.Stop()

		// Run an initial check immediately.
		hc.runCheck(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hc.runCheck(ctx)
			}
		}
	}()
}

// Stop halts the background goroutine and waits for it to finish.
func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
	if hc.done != nil {
		<-hc.done
	}
}

// Status returns the current health status.
func (hc *HealthChecker) Status() HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.status
}

func (hc *HealthChecker) runCheck(ctx context.Context) {
	cfg := hc.svc.Def.HealthCheckCfg
	var checkErr error

	// URL-based check
	if cfg.URL != "" {
		if err := hc.checkURL(ctx, cfg.URL); err != nil {
			checkErr = fmt.Errorf("url check: %w", err)
		}
	}

	// Command-based check (only run if URL check passed or wasn't configured)
	if checkErr == nil && len(cfg.Command) > 0 {
		if err := hc.checkCommand(ctx, cfg.Command); err != nil {
			checkErr = fmt.Errorf("command check: %w", err)
		}
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.status.LastCheck = time.Now()

	if checkErr != nil {
		hc.status.ConsecutiveFailures++
		hc.status.LastError = checkErr.Error()
		if hc.status.ConsecutiveFailures >= unhealthyThreshold {
			hc.status.Healthy = false
			hc.svc.HealthStatus = hc.status
			if hc.svc.Status == StatusRunning || hc.svc.Status == StatusHealthy {
				hc.svc.Status = StatusUnhealthy
			}
		}
	} else {
		hc.status.ConsecutiveFailures = 0
		hc.status.LastError = ""
		hc.status.Healthy = true
		hc.svc.HealthStatus = hc.status
		if hc.svc.Status == StatusRunning || hc.svc.Status == StatusUnhealthy {
			hc.svc.Status = StatusHealthy
		}
	}
}

func (hc *HealthChecker) checkURL(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, hc.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (hc *HealthChecker) checkCommand(ctx context.Context, command []string) error {
	ctx, cancel := context.WithTimeout(ctx, hc.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

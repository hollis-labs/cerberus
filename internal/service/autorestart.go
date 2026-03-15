package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/pausectl"
)

// RestartStats holds the current state of a restart policy for display.
type RestartStats struct {
	RetryCount   int
	CurrentDelay time.Duration
	MaxRetries   int
	Enabled      bool
}

// RestartPolicy manages automatic restart with exponential backoff for a service.
type RestartPolicy struct {
	Enabled      bool
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxRetries   int

	mu           sync.Mutex
	currentDelay time.Duration
	retryCount   int
	stopCh       chan struct{}
}

// NewRestartPolicy creates a RestartPolicy from a ManagedService's config fields.
func NewRestartPolicy(svc *ManagedService) *RestartPolicy {
	initialDelay := 1 * time.Second
	if svc.Def.RestartDelay != "" {
		if d, err := time.ParseDuration(svc.Def.RestartDelay); err == nil && d > 0 {
			initialDelay = d
		}
	}

	maxRetries := 5 // default
	if svc.Def.MaxRestartAttempts > 0 {
		maxRetries = svc.Def.MaxRestartAttempts
	}

	return &RestartPolicy{
		Enabled:      svc.Def.AutoRestart,
		InitialDelay: initialDelay,
		MaxDelay:     60 * time.Second,
		MaxRetries:   maxRetries,
		currentDelay: initialDelay,
		stopCh:       make(chan struct{}),
	}
}

// Watch monitors the service's exited channel and restarts it with exponential backoff.
// This should be called as a goroutine.
func (rp *RestartPolicy) Watch(svc *ManagedService) {
	if !rp.Enabled {
		return
	}

	go func() {
		for {
			// Wait for exit signal
			exited := svc.exited
			if exited == nil {
				return
			}

			select {
			case <-exited:
				// Process exited
			case <-rp.stopCh:
				return
			}

			// Check if auto-restart is paused before proceeding
			if pausectl.IsServicePaused(svc.Def.ID) {
				llog().Info("autorestart.paused", "service", svc.Def.ID,
					"message", "Auto-restart paused, skipping restart")
				// Wait for unpause or stop — poll every 5 seconds
				for pausectl.IsServicePaused(svc.Def.ID) {
					select {
					case <-time.After(5 * time.Second):
						continue
					case <-rp.stopCh:
						return
					}
				}
				llog().Info("autorestart.resumed", "service", svc.Def.ID)
			}

			rp.mu.Lock()
			if rp.retryCount >= rp.MaxRetries {
				rp.mu.Unlock()
				llog().Warn("autorestart.exhausted", "service", svc.Def.ID, "retries", rp.MaxRetries)
				svc.Status = StatusFailed
				svc.Error = fmt.Sprintf("auto-restart gave up after %d retries", rp.MaxRetries)
				return
			}

			delay := rp.currentDelay
			rp.retryCount++
			svc.RestartCount = rp.retryCount

			// Exponential backoff: double delay, cap at MaxDelay
			rp.currentDelay *= 2
			if rp.currentDelay > rp.MaxDelay {
				rp.currentDelay = rp.MaxDelay
			}
			rp.mu.Unlock()

			llog().Info("autorestart.retry", "service", svc.Def.ID, "delay", delay.String(),
				"attempt", rp.retryCount, "max", rp.MaxRetries)

			// Wait for delay or stop signal
			select {
			case <-time.After(delay):
			case <-rp.stopCh:
				return
			}

			if err := svc.Start(); err != nil {
				llog().Warn("autorestart.failed", "service", svc.Def.ID, "error", err.Error())
			}
		}
	}()
}

// Reset clears retry state back to initial values.
func (rp *RestartPolicy) Reset() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.retryCount = 0
	rp.currentDelay = rp.InitialDelay
}

// Stop cancels the watch goroutine.
func (rp *RestartPolicy) Stop() {
	select {
	case <-rp.stopCh:
		// Already closed
	default:
		close(rp.stopCh)
	}
}

// Stats returns the current restart policy state.
func (rp *RestartPolicy) Stats() RestartStats {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return RestartStats{
		RetryCount:   rp.retryCount,
		CurrentDelay: rp.currentDelay,
		MaxRetries:   rp.MaxRetries,
		Enabled:      rp.Enabled,
	}
}

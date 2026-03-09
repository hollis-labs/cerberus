package service

import (
	"fmt"
	"log"
	"sync"
	"time"
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

// NewRestartPolicy creates a RestartPolicy from a Service's config fields.
func NewRestartPolicy(svc *Service) *RestartPolicy {
	initialDelay := 1 * time.Second
	if svc.Def.RestartDelay != "" {
		if d, err := time.ParseDuration(svc.Def.RestartDelay); err == nil && d > 0 {
			initialDelay = d
		}
	}

	return &RestartPolicy{
		Enabled:      svc.Def.AutoRestart,
		InitialDelay: initialDelay,
		MaxDelay:     60 * time.Second,
		MaxRetries:   5,
		currentDelay: initialDelay,
		stopCh:       make(chan struct{}),
	}
}

// Watch monitors the service's exited channel and restarts it with exponential backoff.
// This should be called as a goroutine.
func (rp *RestartPolicy) Watch(svc *Service) {
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

			rp.mu.Lock()
			if rp.retryCount >= rp.MaxRetries {
				rp.mu.Unlock()
				log.Printf("[autorestart] %s: giving up after %d retries", svc.Def.ID, rp.MaxRetries)
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

			log.Printf("[autorestart] %s: restarting in %s (attempt %d/%d)", svc.Def.ID, delay, rp.retryCount, rp.MaxRetries)

			// Wait for delay or stop signal
			select {
			case <-time.After(delay):
			case <-rp.stopCh:
				return
			}

			if err := svc.Start(); err != nil {
				log.Printf("[autorestart] %s: restart failed: %v", svc.Def.ID, err)
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

package actions

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

// HealthWait polls a URL until it returns 2xx or the timeout expires.
type HealthWait struct {
	resourceID string
	url        string
	timeout    time.Duration
	interval   time.Duration
}

// NewHealthWait creates a health wait action that polls the given URL.
func NewHealthWait(resourceID, url string, timeout time.Duration) *HealthWait {
	return &HealthWait{
		resourceID: resourceID,
		url:        url,
		timeout:    timeout,
		interval:   2 * time.Second,
	}
}

func (a *HealthWait) Name() string { return fmt.Sprintf("health_wait(%s)", a.resourceID) }

func (a *HealthWait) Execute(ctx context.Context, _ *domain.PipelineEnv) error {
	deadline := time.Now().Add(a.timeout)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 5 * time.Second}

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("health check timed out after %s for %s (%s)", a.timeout, a.resourceID, a.url)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		}

		attemptTimeout := 5 * time.Second
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, a.url, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("build health check request for %s (%s): %w", a.resourceID, a.url, err)
		}
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *HealthWait) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil
}

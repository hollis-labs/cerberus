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
	deadline := time.After(a.timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("health check timed out after %s for %s (%s)", a.timeout, a.resourceID, a.url)
		default:
			resp, err := client.Get(a.url)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
			time.Sleep(a.interval)
		}
	}
}

func (a *HealthWait) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil
}

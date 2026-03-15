package service

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/config"
)

// trueBin and falseBin resolve the paths to true/false binaries
// across platforms (macOS uses /usr/bin, Linux uses /bin or /usr/bin).
func findBin(name string) string {
	for _, dir := range []string{"/usr/bin", "/bin"} {
		p := dir + "/" + name
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	// Fall back to PATH lookup
	p, _ := exec.LookPath(name)
	return p
}

func newTestService(cfg config.HealthCheck) *ManagedService {
	return &ManagedService{
		Def: config.ServiceDef{
			ID:             "test-svc",
			HealthCheckCfg: cfg,
		},
		Status: StatusRunning,
	}
}

func TestURLCheckHealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc := newTestService(config.HealthCheck{
		URL:      ts.URL,
		Interval: "100ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	if hc == nil {
		t.Fatal("expected non-nil HealthChecker")
	}

	hc.Start()
	time.Sleep(250 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	if !status.Healthy {
		t.Errorf("expected healthy, got unhealthy: %s", status.LastError)
	}
	if status.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 consecutive failures, got %d", status.ConsecutiveFailures)
	}
	if status.LastCheck.IsZero() {
		t.Error("expected LastCheck to be set")
	}
	if svc.Status != StatusHealthy {
		t.Errorf("expected service status healthy, got %s", svc.Status)
	}
}

func TestURLCheckUnhealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	svc := newTestService(config.HealthCheck{
		URL:      ts.URL,
		Interval: "50ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	hc.Start()
	// Wait enough for at least 3 failures (initial + 2 ticks at 50ms intervals)
	time.Sleep(350 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	if status.Healthy {
		t.Error("expected unhealthy")
	}
	if status.ConsecutiveFailures < unhealthyThreshold {
		t.Errorf("expected at least %d consecutive failures, got %d", unhealthyThreshold, status.ConsecutiveFailures)
	}
	if status.LastError == "" {
		t.Error("expected LastError to be set")
	}
	if svc.Status != StatusUnhealthy {
		t.Errorf("expected service status unhealthy, got %s", svc.Status)
	}
}

func TestCommandCheckHealthy(t *testing.T) {
	svc := newTestService(config.HealthCheck{
		Command:  []string{findBin("true")},
		Interval: "100ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	if hc == nil {
		t.Fatal("expected non-nil HealthChecker")
	}

	hc.Start()
	time.Sleep(250 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	if !status.Healthy {
		t.Errorf("expected healthy, got unhealthy: %s", status.LastError)
	}
}

func TestCommandCheckUnhealthy(t *testing.T) {
	svc := newTestService(config.HealthCheck{
		Command:  []string{findBin("false")},
		Interval: "50ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	hc.Start()
	time.Sleep(350 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	if status.Healthy {
		t.Error("expected unhealthy")
	}
	if status.ConsecutiveFailures < unhealthyThreshold {
		t.Errorf("expected at least %d consecutive failures, got %d", unhealthyThreshold, status.ConsecutiveFailures)
	}
}

func TestConsecutiveFailureCounting(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	svc := newTestService(config.HealthCheck{
		URL:      ts.URL,
		Interval: "50ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	hc.Start()
	// Let it run: initial check + a few ticks for healthy, then failures
	time.Sleep(500 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	// After the first 2 healthy checks, failures should accumulate
	if status.ConsecutiveFailures == 0 {
		t.Error("expected some consecutive failures")
	}
}

func TestIntervalAndTimeoutParsing(t *testing.T) {
	svc := newTestService(config.HealthCheck{
		URL:      "http://localhost:1/nope",
		Interval: "10s",
		Timeout:  "3s",
	})

	hc := NewHealthChecker(svc)
	if hc == nil {
		t.Fatal("expected non-nil HealthChecker")
	}

	if hc.interval != 10*time.Second {
		t.Errorf("expected interval 10s, got %v", hc.interval)
	}
	if hc.timeout != 3*time.Second {
		t.Errorf("expected timeout 3s, got %v", hc.timeout)
	}
}

func TestIntervalAndTimeoutDefaults(t *testing.T) {
	svc := newTestService(config.HealthCheck{
		URL: "http://localhost:1/nope",
	})

	hc := NewHealthChecker(svc)
	if hc == nil {
		t.Fatal("expected non-nil HealthChecker")
	}

	if hc.interval != defaultInterval {
		t.Errorf("expected default interval %v, got %v", defaultInterval, hc.interval)
	}
	if hc.timeout != defaultTimeout {
		t.Errorf("expected default timeout %v, got %v", defaultTimeout, hc.timeout)
	}
}

func TestNewHealthCheckerReturnsNilWhenNoConfig(t *testing.T) {
	svc := newTestService(config.HealthCheck{})
	hc := NewHealthChecker(svc)
	if hc != nil {
		t.Error("expected nil HealthChecker when no health check configured")
	}
}

func TestStopCleanlyShutsDown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc := newTestService(config.HealthCheck{
		URL:      ts.URL,
		Interval: "50ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	hc.Start()
	time.Sleep(100 * time.Millisecond)

	// Stop should return promptly without blocking indefinitely.
	done := make(chan struct{})
	go func() {
		hc.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}

func TestBothURLAndCommandMustPass(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// URL healthy, command unhealthy
	svc := newTestService(config.HealthCheck{
		URL:      ts.URL,
		Command:  []string{findBin("false")},
		Interval: "50ms",
		Timeout:  "2s",
	})

	hc := NewHealthChecker(svc)
	hc.Start()
	time.Sleep(350 * time.Millisecond)
	hc.Stop()

	status := hc.Status()
	if status.Healthy {
		t.Error("expected unhealthy when command fails even though URL succeeds")
	}
}

package service

import (
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestNewRestartPolicyDefaults(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart: true,
		},
	}

	rp := NewRestartPolicy(svc)

	if !rp.Enabled {
		t.Error("expected Enabled to be true")
	}
	if rp.InitialDelay != 1*time.Second {
		t.Errorf("expected InitialDelay 1s, got %s", rp.InitialDelay)
	}
	if rp.MaxDelay != 60*time.Second {
		t.Errorf("expected MaxDelay 60s, got %s", rp.MaxDelay)
	}
	if rp.MaxRetries != 5 {
		t.Errorf("expected MaxRetries 5, got %d", rp.MaxRetries)
	}
}

func TestNewRestartPolicyDisabled(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart: false,
		},
	}

	rp := NewRestartPolicy(svc)

	if rp.Enabled {
		t.Error("expected Enabled to be false")
	}
}

func TestNewRestartPolicyCustomDelay(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart:  true,
			RestartDelay: "5s",
		},
	}

	rp := NewRestartPolicy(svc)

	if rp.InitialDelay != 5*time.Second {
		t.Errorf("expected InitialDelay 5s, got %s", rp.InitialDelay)
	}
}

func TestNewRestartPolicyInvalidDelay(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart:  true,
			RestartDelay: "not-a-duration",
		},
	}

	rp := NewRestartPolicy(svc)

	// Should fall back to default 1s
	if rp.InitialDelay != 1*time.Second {
		t.Errorf("expected InitialDelay 1s for invalid delay, got %s", rp.InitialDelay)
	}
}

func TestExponentialBackoff(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart: true,
		},
	}

	rp := NewRestartPolicy(svc)

	// Simulate the backoff progression: 1s -> 2s -> 4s -> 8s -> 16s -> 32s -> 60s (capped)
	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second, // capped at MaxDelay
	}

	for i, exp := range expected {
		rp.mu.Lock()
		got := rp.currentDelay
		rp.mu.Unlock()

		if got != exp {
			t.Errorf("step %d: expected delay %s, got %s", i, exp, got)
		}

		// Simulate what Watch does: record current delay, then double + cap
		rp.mu.Lock()
		rp.currentDelay *= 2
		if rp.currentDelay > rp.MaxDelay {
			rp.currentDelay = rp.MaxDelay
		}
		rp.retryCount++
		rp.mu.Unlock()
	}
}

func TestMaxRetriesExhausted(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			ID:          "test-svc",
			AutoRestart: true,
		},
	}

	rp := NewRestartPolicy(svc)

	// Set retry count to max
	rp.mu.Lock()
	rp.retryCount = rp.MaxRetries
	rp.mu.Unlock()

	stats := rp.Stats()
	if stats.RetryCount != 5 {
		t.Errorf("expected retry count 5, got %d", stats.RetryCount)
	}

	// Verify that at MaxRetries, the Watch loop would give up
	if stats.RetryCount < stats.MaxRetries {
		t.Error("expected retryCount >= MaxRetries to indicate exhaustion")
	}
}

func TestReset(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart:  true,
			RestartDelay: "2s",
		},
	}

	rp := NewRestartPolicy(svc)

	// Simulate some retries
	rp.mu.Lock()
	rp.retryCount = 3
	rp.currentDelay = 16 * time.Second
	rp.mu.Unlock()

	rp.Reset()

	stats := rp.Stats()
	if stats.RetryCount != 0 {
		t.Errorf("expected retry count 0 after reset, got %d", stats.RetryCount)
	}
	if stats.CurrentDelay != 2*time.Second {
		t.Errorf("expected delay reset to 2s, got %s", stats.CurrentDelay)
	}
}

func TestStopCancelsWatch(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			ID:          "test-svc",
			AutoRestart: true,
		},
		exited: make(chan struct{}),
	}

	rp := NewRestartPolicy(svc)

	// Start watching
	rp.Watch(svc)

	// Stop should close the stopCh and the goroutine should exit
	rp.Stop()

	// Verify stopCh is closed
	select {
	case <-rp.stopCh:
		// Good, channel is closed
	default:
		t.Error("expected stopCh to be closed after Stop()")
	}

	// Calling Stop again should not panic
	rp.Stop()
}

func TestStopIdempotent(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart: true,
		},
	}

	rp := NewRestartPolicy(svc)

	// Multiple stops should not panic
	rp.Stop()
	rp.Stop()
	rp.Stop()
}

func TestStats(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart:  true,
			RestartDelay: "3s",
		},
	}

	rp := NewRestartPolicy(svc)

	stats := rp.Stats()
	if !stats.Enabled {
		t.Error("expected Enabled true")
	}
	if stats.RetryCount != 0 {
		t.Errorf("expected RetryCount 0, got %d", stats.RetryCount)
	}
	if stats.CurrentDelay != 3*time.Second {
		t.Errorf("expected CurrentDelay 3s, got %s", stats.CurrentDelay)
	}
	if stats.MaxRetries != 5 {
		t.Errorf("expected MaxRetries 5, got %d", stats.MaxRetries)
	}
}

func TestParseDurationVariants(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"1s", 1 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"2m", 2 * time.Minute},
		{"1m30s", 90 * time.Second},
		{"", 1 * time.Second},        // default
		{"invalid", 1 * time.Second}, // fallback to default
	}

	for _, tc := range cases {
		svc := &ManagedService{
			Def: config.ServiceDef{
				AutoRestart:  true,
				RestartDelay: tc.input,
			},
		}
		rp := NewRestartPolicy(svc)
		if rp.InitialDelay != tc.expected {
			t.Errorf("RestartDelay=%q: expected %s, got %s", tc.input, tc.expected, rp.InitialDelay)
		}
	}
}

func TestWatchDisabledDoesNothing(t *testing.T) {
	svc := &ManagedService{
		Def: config.ServiceDef{
			AutoRestart: false,
		},
		exited: make(chan struct{}),
	}

	rp := NewRestartPolicy(svc)
	// Watch with disabled policy should return immediately without starting goroutine
	rp.Watch(svc)

	// Close exited to simulate process exit — nothing should happen
	close(svc.exited)

	// Give a moment to confirm no panic or unexpected behavior
	time.Sleep(10 * time.Millisecond)
}

func TestStatusFailedString(t *testing.T) {
	if StatusFailed.String() != "failed" {
		t.Errorf("expected StatusFailed to be 'failed', got %q", StatusFailed.String())
	}
}

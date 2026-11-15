// Package hostmon is the host monitor of the Substrate build: the scheduled probes, the status
// they derive, the janitor that compacts samples, and the writer that carries their class B
// rows to the Telemetry store (docs/substrate-port-plan.md sections 4.2 and 5.1). It is the
// old daemon's internal/cerbapi host monitor, moved; internal/hosts holds the logic and is
// carried unchanged.
//
// It runs only in supervise mode. Observe mode starts none of it.
package hostmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// DefaultPendingLimit bounds the writes held in memory while the Telemetry store fails.
const DefaultPendingLimit = 1000

// SampleStore is the part of hosts.SampleStore the monitor uses. *hosts.SampleStore is it.
type SampleStore interface {
	AppendSample(ctx context.Context, x hosts.Sample) error
	Recent(ctx context.Context, host string, n int) ([]hosts.Sample, error)
	PutStatus(ctx context.Context, st hosts.HostStatus) error
	Status(ctx context.Context, host string) (hosts.HostStatus, bool, error)
	AllStatus(ctx context.Context) (map[string]hosts.HostStatus, error)
	ForgetStatus(ctx context.Context, keep map[string]bool) error
	AppendEvent(ctx context.Context, e hosts.Event) (int64, error)
	Compact(ctx context.Context, now time.Time, r hosts.Retention) (hosts.CompactReport, error)
}

// pendingWrite is one class B write held while the store fails.
type pendingWrite struct {
	what string
	host string
	run  func(ctx context.Context) error
}

// Telemetry carries the monitor's writes to the Telemetry store. A write the store refuses is
// held in memory, in order, and retried before the next write; at most limit are held, and
// when full the oldest is dropped and counted. There is no durable spool (plan section 5.1):
// the gap is visible in the drop counter and in /healthz, and supervision never waits on it.
//
// A write the store rejects for its content (a late sample) is not held: retrying it cannot
// succeed.
type Telemetry struct {
	logger *slog.Logger
	limit  int

	mu      sync.Mutex
	pending []pendingWrite
	dropped int64
	lastErr string
}

// NewTelemetry builds the writer. limit <= 0 is DefaultPendingLimit.
func NewTelemetry(limit int, logger *slog.Logger) *Telemetry {
	if limit <= 0 {
		limit = DefaultPendingLimit
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Telemetry{logger: logger, limit: limit}
}

// Write runs one write after any held ones. If the store refuses it, it is held.
func (t *Telemetry) Write(ctx context.Context, what, host string, run func(ctx context.Context) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = append(t.pending, pendingWrite{what: what, host: host, run: run})
	t.drainLocked(ctx)
	for len(t.pending) > t.limit {
		t.pending = t.pending[1:]
		t.dropped++
		if t.dropped == 1 || t.dropped%100 == 0 {
			t.logger.Warn("daemon.host_monitor.telemetry_dropped", "dropped", t.dropped, "held", len(t.pending))
		}
	}
}

// Flush retries the held writes once, in order, stopping at the first the store refuses.
func (t *Telemetry) Flush(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.drainLocked(ctx)
}

func (t *Telemetry) drainLocked(ctx context.Context) {
	for len(t.pending) > 0 {
		w := t.pending[0]
		err := w.run(ctx)
		switch {
		case err == nil:
			t.lastErr = ""
		case errors.Is(err, hosts.ErrLateSample):
			// The clock stepped back: not stored, and never retried.
			t.logger.Warn("daemon.host_monitor.sample_late", "host", w.host, "error", redact.Text(err.Error()))
		default:
			msg := redact.Text(err.Error())
			if t.lastErr == "" {
				t.logger.Warn("daemon.host_monitor.telemetry_unavailable", "write", w.what, "host", w.host, "error", msg)
			}
			t.lastErr = msg
			return
		}
		t.pending = t.pending[1:]
	}
}

// TelemetryState is the writer's counters.
type TelemetryState struct {
	Held    int
	Dropped int64
	LastErr string
}

// State reports the counters.
func (t *Telemetry) State() TelemetryState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TelemetryState{Held: len(t.pending), Dropped: t.dropped, LastErr: t.lastErr}
}

// Check is the /healthz check: an error while writes are held, naming the held and dropped
// counts; nil once the store takes them again.
func (t *Telemetry) Check(context.Context) error {
	st := t.State()
	if st.Held == 0 {
		return nil
	}
	return fmt.Errorf("telemetry store unavailable: %d host-monitor writes held in memory, %d dropped (oldest first): %s",
		st.Held, st.Dropped, st.LastErr)
}
var _ SampleStore = (*hosts.SampleStore)(nil)

package pluginhost

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// Bounds on what one operation's telemetry can hold, so a chatty or hostile
// plugin cannot grow an audit record without limit.
const (
	maxTelemetryEvents = 32
	maxStderrLines     = 32
	maxTelemetryField  = 512
	maxTelemetryBytes  = 8 << 10
)

// Telemetry is what a plugin reported during one operation, bounded and
// redacted: the events in its result, and the stderr lines it wrote while
// the call ran.
type Telemetry struct {
	Events       []plugin.TelemetryEvent
	Stderr       []string
	SharedStderr bool
	Truncated    bool
}

// Empty reports whether nothing was collected.
func (t Telemetry) Empty() bool {
	return len(t.Events) == 0 && len(t.Stderr) == 0 && !t.Truncated
}

// Collector gathers one operation's telemetry. The audit layer puts one in
// the context before the call; the manager fills it; the audit layer reads
// it into the outcome record. The plugin never sees it.
type Collector struct {
	mu    sync.Mutex
	t     Telemetry
	bytes int
}

type collectorKey struct{}

// WithTelemetry returns a context carrying a new collector.
func WithTelemetry(ctx context.Context) (context.Context, *Collector) {
	c := &Collector{}
	return context.WithValue(ctx, collectorKey{}, c), c
}

func collectorFrom(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}

// Snapshot is the telemetry collected so far.
func (c *Collector) Snapshot() Telemetry {
	if c == nil {
		return Telemetry{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	t.Events = append([]plugin.TelemetryEvent(nil), c.t.Events...)
	t.Stderr = append([]string(nil), c.t.Stderr...)
	return t
}

// fit trims a field to the per-field bound and reports whether the record's
// byte budget has room for it.
func (c *Collector) fit(s string) (string, bool) {
	if len(s) > maxTelemetryField {
		s = s[:maxTelemetryField]
		c.t.Truncated = true
	}
	if c.bytes+len(s) > maxTelemetryBytes {
		c.t.Truncated = true
		return "", false
	}
	c.bytes += len(s)
	return s, true
}

func (c *Collector) addEvents(events []plugin.TelemetryEvent, r redact.Redactor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range events {
		if len(c.t.Events) >= maxTelemetryEvents {
			c.t.Truncated = true
			return
		}
		kind, ok1 := c.fit(r.Text(e.Kind))
		message, ok2 := c.fit(r.Text(e.Message))
		target, ok3 := c.fit(r.Text(e.Target))
		if !ok1 || !ok2 || !ok3 {
			return
		}
		c.t.Events = append(c.t.Events, plugin.TelemetryEvent{Kind: kind, Message: message, Target: target})
	}
}

func (c *Collector) addStderr(line string, shared bool, r redact.Redactor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if shared {
		c.t.SharedStderr = true
	}
	if len(c.t.Stderr) >= maxStderrLines {
		c.t.Truncated = true
		return
	}
	if kept, ok := c.fit(r.Text(line)); ok {
		c.t.Stderr = append(c.t.Stderr, kept)
	}
}

// stderrTap sits between a plugin's stderr and wherever it was going. It
// passes every byte through unchanged, and hands each complete line to the
// collectors of the calls running at that moment. With more than one call in
// flight a line cannot be attributed to one of them, so each gets it, marked
// shared.
type stderrTap struct {
	mu       sync.Mutex
	next     io.Writer
	partial  []byte
	attached map[*Collector]int
	redactor redact.Redactor
}

func newStderrTap() *stderrTap {
	return &stderrTap{next: io.Discard, attached: map[*Collector]int{}}
}

func (t *stderrTap) wrap(next io.Writer) io.Writer {
	t.mu.Lock()
	defer t.mu.Unlock()
	if next != nil {
		t.next = next
	}
	return t
}

func (t *stderrTap) setRedactor(r redact.Redactor) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.redactor = r
}

// attach starts feeding stderr lines to c until the returned func runs.
func (t *stderrTap) attach(c *Collector) func() {
	t.mu.Lock()
	t.attached[c]++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		if t.attached[c]--; t.attached[c] <= 0 {
			delete(t.attached, c)
		}
		t.mu.Unlock()
	}
}

func (t *stderrTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	next := t.next
	t.partial = append(t.partial, p...)
	var lines []string
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(bytes.TrimRight(t.partial[:i], "\r")))
		t.partial = t.partial[i+1:]
	}
	if len(t.partial) > maxTelemetryField*8 {
		lines = append(lines, string(t.partial))
		t.partial = nil
	}
	collectors := make([]*Collector, 0, len(t.attached))
	for c := range t.attached {
		collectors = append(collectors, c)
	}
	r := t.redactor
	t.mu.Unlock()

	shared := len(collectors) > 1
	for _, line := range lines {
		for _, c := range collectors {
			c.addStderr(line, shared, r)
		}
	}
	return next.Write(p)
}

type stderrTapKey struct{}

// withStderrTap tells the transport, through the launch context, to route the
// plugin's stderr through tap.
func withStderrTap(ctx context.Context, tap *stderrTap) context.Context {
	return context.WithValue(ctx, stderrTapKey{}, tap)
}

func stderrTapFrom(ctx context.Context) *stderrTap {
	tap, _ := ctx.Value(stderrTapKey{}).(*stderrTap)
	return tap
}

package scheduling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/secret"
	"github.com/hollis-labs/libs/core/util/scheduler"
)

// SecretResolver is host-owned, not a caller-supplied provider selector.
// Resolve must authorize the alias for this exact target and return no raw
// provider error text. Production construction does not install a resolver.
type SecretResolver interface {
	Resolve(context.Context, Request, EnvReference) (string, error)
}

// SecretBinding restricts an alias to one authored target and environment name.
type SecretBinding struct {
	Target            Target
	Env, Service, Key string
}
type BoundSecrets struct {
	reader   secret.Reader
	bindings map[string]SecretBinding
}

// NewBoundSecrets snapshots permitted alias mappings. Value rotation remains
// inside the existing reader; callers cannot substitute service/key/target.
func NewBoundSecrets(reader secret.Reader, bindings map[string]SecretBinding) *BoundSecrets {
	snapshot := make(map[string]SecretBinding, len(bindings))
	for name, binding := range bindings {
		snapshot[name] = binding
	}
	return &BoundSecrets{reader: reader, bindings: snapshot}
}

func (b BoundSecrets) Resolve(ctx context.Context, req Request, ref EnvReference) (string, error) {
	binding, ok := b.bindings[ref.Ref]
	if !ok || b.reader == nil || binding.Target != req.Job.Target || binding.Env != ref.Env || binding.Service == "" || binding.Key == "" {
		return "", errors.New("secret binding unavailable")
	}
	return b.reader.Get(ctx, binding.Service, binding.Key)
}

// DeliveryExecutor extends the existing serving executor; it must validate the
// entire frozen target before admission and never silently ignore RunIO.
type DeliveryExecutor interface {
	ExecuteDelivery(context.Context, Target, Admission, *Delivery) error
}
type DeliveryOptions struct {
	Secrets                 SecretResolver
	StreamBytes, TotalBytes int
}

// Delivery is private per-fire IO. Its environment never enters DTOs/history.
// It implements domain.PipelineRunIO without exposing credentials to formatting.
type Delivery struct {
	core           *Core
	job            Job
	fire           scheduler.Job
	scope          *redact.Scope
	mu             sync.Mutex
	env            map[string]string
	stdout, stderr *logWriter
	prepared       bool
}

func (d *Delivery) String() string               { return "scheduled delivery (private environment)" }
func (d *Delivery) GoString() string             { return d.String() }
func (d *Delivery) MarshalJSON() ([]byte, error) { return []byte(`"scheduled delivery"`), nil }
func (d *Delivery) prepare(ctx context.Context, req Request) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.prepared {
		return nil
	}
	env := map[string]string{}
	forms := []string{}
	if len(d.job.EnvRefs) > 32 {
		return redact.Guidance("too many scheduled environment references; nothing was sent")
	}
	for _, ref := range d.job.EnvRefs {
		// Environment control variables can change the executable or credential
		// search path; a names-only alias does not grant these capabilities.
		if unsafeRunEnv(ref.Env) || d.core.delivery.Secrets == nil {
			return redact.Guidance("scheduled secret binding is unavailable; nothing was sent")
		}
		value, err := d.core.delivery.Secrets.Resolve(ctx, req, ref)
		if err != nil {
			return redact.Guidance("scheduled secret resolution refused; nothing was sent")
		}
		if len(value) > 4096 || strings.IndexByte(value, 0) >= 0 || !d.scope.Add(ref.Ref, value) {
			return redact.Guidance("scheduled secret cannot be safely protected; nothing was sent")
		}
		env[ref.Env] = value
		forms = append(forms, redact.Forms(value)...)
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	if err := d.core.beginLogs(ctx, d.job, d.fire); err != nil {
		return redact.Guidance("scheduled log storage is unavailable; nothing was sent")
	}
	d.env = env
	d.stdout = &logWriter{delivery: d, stream: "stdout", forms: forms}
	d.stderr = &logWriter{delivery: d, stream: "stderr", forms: forms}
	d.prepared = true
	return nil
}
func unsafeRunEnv(name string) bool {
	switch name {
	case "PATH", "HOME", "SHELL", "ENV", "BASH_ENV", "ZDOTDIR", "GOPATH", "GOWORK", "GOTOOLCHAIN", "SSH_AUTH_SOCK", "PYTHONPATH", "NODE_OPTIONS":
		return true
	}
	return strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "DOCKER_")
}
func (d *Delivery) Environ(base []string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.prepared {
		return nil, errors.New("scheduled delivery was not admitted")
	}
	env := make(map[string]string, len(base)+len(d.env))
	for _, entry := range base {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	for k, v := range d.env {
		env[k] = v
	}
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, k+"="+env[k])
	}
	return out, nil
}
func (d *Delivery) Stdout() io.Writer { return d.stdout }
func (d *Delivery) Stderr() io.Writer { return d.stderr }
func (d *Delivery) close(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.prepared {
		return
	}
	d.stdout.close(ctx)
	d.stderr.close(ctx)
	if d.stdout.failed || d.stderr.failed {
		_, _ = d.core.db.ExecContext(ctx, `UPDATE cerberus_schedule_logs SET capture_error=1 WHERE fire_id=?`, d.fire.FireID)
	}
	// The pipeline is synchronous. Dropping references is not a promise to
	// securely erase strings from Go's garbage-collected heap.
	d.env = nil
}

// Captured output reaches the database in batches, not one transaction per Write:
// already-redacted text is buffered and appended when it reaches logFlushBytes, when
// logFlushInterval has passed since it was first buffered, and when the stream
// closes. A process crash can therefore lose at most that much output per stream
// (decision on CW-20261016-0228: 4KiB or 250ms of already-redacted job output; this
// is job log capture, not the audit chain). The redaction hold-back is unchanged and
// is applied before buffering, and the scope redacts the whole batch when it is
// appended, so a value split across Writes is caught as well as one in a single Write.
const (
	logFlushBytes    = 4096
	logFlushInterval = 250 * time.Millisecond
	// logCarryBytes is the least that stays buffered at a size or timer flush, so a
	// token the redaction net matches (a bearer token, a provider key, an assignment
	// value) that straddles the boundary is seen whole by the next append. Only close
	// flushes it all. logPEMHoldBytes bounds how long an open private-key block is held
	// back for its END line.
	logCarryBytes   = 1024
	logPEMHoldBytes = 16 << 10
)

type logWriter struct {
	mu       sync.Mutex
	delivery *Delivery
	stream   string
	forms    []string
	pending  []byte
	out      []byte
	timer    *time.Timer
	closed   bool
	failed   bool
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	size := len(p)
	// Bound raw carry and transient input independently of producer chunk size.
	for len(p) > 0 {
		n := min(len(p), 4096)
		w.pending = append(w.pending, p[:n]...)
		p = p[n:]
		w.flush(context.Background(), false)
	}
	return size, nil // drain even after storage failure; database writes can delay it
}
func (w *logWriter) close(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.flush(ctx, true)
		w.closed = true
		w.pending = nil
	}
}
func (w *logWriter) flush(ctx context.Context, final bool) {
	hold := 1
	for _, f := range w.forms {
		hold = max(hold, len(f))
	}
	var safe strings.Builder
	for len(w.pending) > 0 && (final || len(w.pending) >= hold) {
		matched := 0
		for _, f := range w.forms {
			if len(w.pending) >= len(f) && strings.HasPrefix(string(w.pending), f) {
				matched = len(f)
				break
			}
		}
		if matched > 0 {
			safe.WriteString(redact.Marker)
			w.pending = w.pending[matched:]
		} else {
			safe.WriteByte(w.pending[0])
			w.pending = w.pending[1:]
		}
	}
	w.out = append(w.out, safe.String()...)
	switch {
	case len(w.out) == 0:
	case final || len(w.out) >= logFlushBytes:
		w.appendOut(ctx, final)
	case w.timer == nil:
		// The first buffered bytes start the clock: they reach the database within
		// logFlushInterval even if the job then goes quiet.
		interval := w.delivery.core.logFlushInterval()
		w.timer = time.AfterFunc(interval, w.flushOnTimer)
	}
}

// appendOut writes the buffered, already-redacted text in one append and stops the
// timer. The scope redacts the whole batch here, after every value it knows has been
// registered. Unless the stream is closing, the last (longest scope value - 1) bytes
// stay buffered, so a value that straddles the batch boundary is seen whole by the
// next append; that carry is bounded by the longest secret, as the hold-back is.
// Callers hold w.mu.
func (w *logWriter) appendOut(ctx context.Context, final bool) {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if len(w.out) == 0 {
		return
	}
	cut := len(w.out)
	if !final {
		cut = len(w.out) - max(logCarryBytes, w.delivery.scope.MaxValueLen()-1)
		cut = holdPEM(w.out, cut)
		for cut > 0 && cut < len(w.out) && !utf8.RuneStart(w.out[cut]) {
			cut--
		}
	}
	if cut <= 0 {
		// Nothing can be released yet; look again when more output arrives or at close.
		w.timer = time.AfterFunc(w.delivery.core.logFlushInterval(), w.flushOnTimer)
		return
	}
	// Redaction runs on the buffer before any byte reaches the database, and there is
	// no raw path: if the append fails the chunk is dropped and the stream is marked
	// failed (capture_error), never retried unredacted.
	text := w.delivery.scope.Text(string(w.out[:cut]))
	w.out = append([]byte(nil), w.out[cut:]...)
	if err := w.delivery.core.appendLog(ctx, w.delivery.job, w.delivery.fire, w.stream, text); err != nil {
		w.failed = true
	}
	if len(w.out) > 0 {
		w.timer = time.AfterFunc(w.delivery.core.logFlushInterval(), w.flushOnTimer)
	}
}

func (w *logWriter) flushOnTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = nil
	if w.closed {
		return
	}
	w.appendOut(context.Background(), false)
}

// holdPEM moves cut back so it never falls inside a private-key block: the net
// matches a block only whole, with its END line, so the block is released together or
// held. An open block (no END yet) is held while it is no longer than logPEMHoldBytes.
func holdPEM(out []byte, cut int) int {
	begin, end := []byte("-----BEGIN "), []byte("-----END ")
	for from := 0; from < len(out); {
		i := bytes.Index(out[from:], begin)
		if i < 0 {
			break
		}
		i += from
		e := bytes.Index(out[i:], end)
		if e < 0 {
			if len(out)-i <= logPEMHoldBytes {
				cut = min(cut, i)
			}
			break
		}
		blockEnd := len(out)
		if tail := bytes.Index(out[i+e+len(end):], []byte("-----")); tail >= 0 {
			blockEnd = i + e + len(end) + tail + len("-----")
		}
		if i < cut && cut < blockEnd {
			cut = i
		}
		from = blockEnd
	}
	return cut
}

// logFlushInterval is the interval after which buffered output is appended; tests
// shorten it.
func (c *Core) logFlushInterval() time.Duration {
	if c.flushInterval > 0 {
		return c.flushInterval
	}
	return logFlushInterval
}

// Default limits bound retained sanitized bytes, including inaccessible orphans.
const defaultStreamBytes = 64 << 10
const defaultTotalLogBytes = 4 << 20

func (c *Core) logLimits() (int, int) {
	a, b := c.delivery.StreamBytes, c.delivery.TotalBytes
	if a <= 0 {
		a = defaultStreamBytes
	}
	if b <= 0 {
		b = defaultTotalLogBytes
	}
	return min(a, defaultStreamBytes), min(b, defaultTotalLogBytes)
}

// Keep these dependencies out of the public environment/schema representation.
var _ fmt.Stringer = (*Delivery)(nil)

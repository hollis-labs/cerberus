package scheduling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/secret"
	"github.com/hollis-labs/libs/util/scheduler"
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

type logWriter struct {
	mu       sync.Mutex
	delivery *Delivery
	stream   string
	forms    []string
	pending  []byte
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
	if safe.Len() > 0 {
		if err := w.delivery.core.appendLog(ctx, w.delivery.job, w.delivery.fire, w.stream, w.delivery.scope.Text(safe.String())); err != nil {
			w.failed = true
		}
	}
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

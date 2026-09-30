package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// A plugin is supervised (P5-d): every protocol call has a deadline, a
// plugin that misses one or sends more than the host reads is stopped with
// its whole process group, and a stopped plugin is restarted with backoff
// until it has been restarted too often, when the host gives up loudly.

// ErrDeadlineExceeded is a plugin that did not answer within its deadline.
var ErrDeadlineExceeded = errors.New("the plugin did not answer within its deadline")

// DeadlineError is a missed deadline, and what the host did about it.
type DeadlineError struct {
	Connector string
	// Phase is init, load, health, unload or call.
	Phase     string
	Operation string
	Timeout   time.Duration
	Effect    contract.Effect
}

func (e *DeadlineError) Error() string {
	what := e.Phase
	if e.Operation != "" {
		what = e.Operation
	}
	msg := fmt.Sprintf("plugin %q did not answer %s within %s; Cerberus stopped it (its whole process group)", e.Connector, what, e.Timeout)
	if e.Phase == "call" {
		msg += " and is restarting it"
		if e.Effect != contract.EffectRead && e.Effect != contract.EffectReadSensitive {
			msg += "; the operation may have partly run, so check the target's state before retrying"
		}
	}
	return msg
}

func (e *DeadlineError) Unwrap() error { return ErrDeadlineExceeded }

// OutputTooLargeError is a read whose result is over the plugin's cap, so it
// is withheld rather than returned cut short.
type OutputTooLargeError struct {
	Connector string
	Operation string
	Bytes     int
	Cap       int
}

func (e *OutputTooLargeError) Error() string {
	return fmt.Sprintf("plugin %q %s returned %s, over its %s cap, so the result is withheld; narrow the request, or raise limits.max_result_bytes for the plugin in %s",
		e.Connector, e.Operation, sizeOf(e.Bytes), sizeOf(e.Cap), ConnectorConfigFilename)
}

// StoppedError is a call to a plugin the host stopped while it ran.
type StoppedError struct {
	Connector string
	Reason    string
}

func (e *StoppedError) Error() string {
	return fmt.Sprintf("plugin %q was stopped while this call ran (%s); it is being restarted, so retry shortly", e.Connector, e.Reason)
}

func (e *StoppedError) Unwrap() error { return ErrNotLoaded }

// OutputCap says what the host did to an over-cap result.
type OutputCap struct {
	// Action is truncated (text, with a marker) or withheld (structured).
	Action string `json:"action"`
	Bytes  int    `json:"bytes"`
	Cap    int    `json:"cap"`
}

// RestartEvent is one supervision step, for the caller to record: a plugin
// stopped, restarted, failed to restart, or given up on.
type RestartEvent struct {
	ID string
	// Kind is stopped, restarted, restart_failed or gave_up.
	Kind    string
	Reason  string
	Attempt int
	Err     error
}

// WithRestartObserver receives every supervision step. The daemon records
// each and notifies the operator when the host gives up.
func WithRestartObserver(observe func(RestartEvent)) ManagerOption {
	return func(m *Manager) { m.observe = observe }
}

// Restart policy.
const (
	restartWindow = 10 * time.Minute
	maxRestarts   = 3
)

var restartBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

// supervision is the manager's restart state.
type supervision struct {
	mu       sync.Mutex
	restarts map[string][]time.Time
	gaveUp   map[string]string
	// held are plugins an operator unloaded or removed, which a pending
	// restart must not bring back.
	held map[string]bool
	// pending are plugins in a restart's backoff, stopped and not yet
	// loaded again, so a caller waiting on one is told to retry, not to
	// load it.
	pending map[string]bool
}

type killable interface{ Kill() }
type exitWatcher interface{ Exited() <-chan struct{} }
type processGroup interface{ ProcessGroup() int }

// rpcCall is one protocol call under supervision: its phase (call, or
// another RPC a later host capability adds), its name for messages, its
// deadline and, for an operation, its effect.
type rpcCall struct {
	Phase   string
	Name    string
	Timeout time.Duration
	Effect  contract.Effect
}

// supervisedCall runs one protocol call to a loaded plugin under its
// deadline. A missed deadline, or a reply over the host's message cap,
// stops the plugin with its process group and schedules its restart; a
// call cut off because the plugin was stopped under it says so. Any other
// error is do's own. It is not tied to CallTool: any RPC a plugin serves
// is bounded the same way.
func (m *Manager) supervisedCall(ctx context.Context, id string, lp *loadedPlugin, c rpcCall, do func(context.Context) error) error {
	callCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	err := do(callCtx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		deadline := &DeadlineError{Connector: id, Phase: c.Phase, Operation: c.Name, Timeout: c.Timeout, Effect: c.Effect}
		m.stop(id, lp, fmt.Sprintf("%s did not answer within %s", c.Name, c.Timeout))
		return deadline
	case errors.Is(err, ErrMessageTooLarge):
		m.stop(id, lp, fmt.Sprintf("%s sent a reply over %s", c.Name, sizeOf(MaxMessageBytes)))
		return &OutputTooLargeError{Connector: id, Operation: c.Name, Bytes: MaxMessageBytes, Cap: MaxMessageBytes}
	}
	if reason := lp.stoppedReason(); reason != "" {
		return &StoppedError{Connector: id, Reason: reason}
	}
	return err
}

// markStopped records why lp stopped; only the first reason counts.
func (lp *loadedPlugin) markStopped(reason string) bool {
	lp.stopMu.Lock()
	defer lp.stopMu.Unlock()
	if lp.stopReason != "" {
		return false
	}
	lp.stopReason = reason
	close(lp.done)
	return true
}

func (lp *loadedPlugin) stoppedReason() string {
	lp.stopMu.Lock()
	defer lp.stopMu.Unlock()
	return lp.stopReason
}

// kill stops a process now, with its group where the transport has one.
func kill(p Process) {
	if k, ok := p.(killable); ok {
		k.Kill()
	}
	_ = p.Close()
}

// stop takes lp out of service for reason, kills its process group and
// schedules a restart. A plugin already stopped is left alone.
func (m *Manager) stop(id string, lp *loadedPlugin, reason string) {
	if lp.parent != nil {
		// A write instance (I9) is not restarted: the next write starts one.
		m.stopWriter(id, lp.parent, lp, reason, true)
		return
	}
	if !lp.markStopped(reason) {
		return
	}
	if w := lp.takeWriter(); w != nil && w.markStopped(reason) {
		go kill(w.process)
	}
	m.mu.Lock()
	if m.running[id] == lp {
		delete(m.running, id)
	}
	m.mu.Unlock()
	go kill(lp.process)
	m.emit(RestartEvent{ID: id, Kind: "stopped", Reason: reason})
	go m.restart(id, reason)
}

func (m *Manager) emit(ev RestartEvent) {
	if m.observe != nil {
		m.observe(ev)
	}
}

// restart brings a stopped plugin back after a backoff, unless it has been
// restarted maxRestarts times within restartWindow, when it gives up and
// the plugin stays unloaded until an operator loads it.
func (m *Manager) restart(id, reason string) {
	s := &m.sup
	s.mu.Lock()
	now := m.clock()
	var recent []time.Time
	for _, t := range s.restarts[id] {
		if now.Sub(t) < restartWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxRestarts {
		s.gaveUp[id] = reason
		s.restarts[id] = recent
		s.mu.Unlock()
		m.emit(RestartEvent{ID: id, Kind: "gave_up", Reason: reason, Attempt: len(recent)})
		return
	}
	recent = append(recent, now)
	s.restarts[id] = recent
	attempt := len(recent)
	s.pending[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	m.sleep(m.backoff(attempt))
	s.mu.Lock()
	held := s.held[id]
	s.mu.Unlock()
	if held || m.Loaded(id) {
		return
	}
	if err := m.load(context.Background(), id, true); err != nil {
		m.emit(RestartEvent{ID: id, Kind: "restart_failed", Reason: reason, Attempt: attempt, Err: err})
		m.restart(id, "restart failed: "+err.Error())
		return
	}
	m.emit(RestartEvent{ID: id, Kind: "restarted", Reason: reason, Attempt: attempt})
}

// restarting reports whether a restart of id is in its backoff or loading.
func (m *Manager) restarting(id string) bool {
	m.sup.mu.Lock()
	defer m.sup.mu.Unlock()
	return m.sup.pending[id] && !m.sup.held[id]
}

func (m *Manager) backoff(attempt int) time.Duration {
	delays := restartBackoff
	if m.restartDelays != nil {
		delays = m.restartDelays
	}
	if attempt-1 < len(delays) {
		return delays[attempt-1]
	}
	return delays[len(delays)-1]
}

// GaveUp is why the host stopped restarting a plugin, or "".
func (m *Manager) GaveUp(id string) string {
	m.sup.mu.Lock()
	defer m.sup.mu.Unlock()
	return m.sup.gaveUp[id]
}

// watch supervises a loaded plugin: a process that exits on its own is
// restarted, and one whose group's resident memory goes over its limit is
// stopped. It ends when lp stops.
func (m *Manager) watch(id string, lp *loadedPlugin) {
	var exited <-chan struct{}
	if w, ok := lp.process.(exitWatcher); ok {
		exited = w.Exited()
	}
	var tick <-chan time.Time
	pg, grouped := lp.process.(processGroup)
	if grouped && lp.limits.MemoryBytes > 0 {
		t := time.NewTicker(m.watchInterval())
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-lp.done:
			return
		case <-exited:
			m.stop(id, lp, "its process exited on its own")
			return
		case <-tick:
			rss, err := m.rss(pg.ProcessGroup())
			if err == nil && rss > lp.limits.MemoryBytes {
				m.stop(id, lp, fmt.Sprintf("its process group's resident memory, %s, went over its %s limit", sizeOf(int(rss)), sizeOf(int(lp.limits.MemoryBytes))))
				return
			}
		}
	}
}

func (m *Manager) watchInterval() time.Duration {
	if m.memoryInterval > 0 {
		return m.memoryInterval
	}
	return 10 * time.Second
}

// groupRSS is the resident size of every process in group pgid, from
// /bin/ps by absolute path (the daemon's PATH is launchd's). Best effort:
// it polls, so a spike between polls is not seen.
func groupRSS(pgid int) (int64, error) {
	out, err := exec.Command("/bin/ps", "-A", "-o", "pgid=,rss=").Output()
	if err != nil {
		return 0, err
	}
	var kib int64
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if g, _ := strconv.Atoi(f[0]); g == pgid {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			kib += n
		}
	}
	return kib << 10, nil
}

// capOutput applies the plugin's result cap to an operation's result.
// Text is cut to the cap with a visible marker, for every effect. Over-cap
// structured content is refused for a read, which ran nothing and can be
// narrowed, and withheld with a note for anything else, which already ran:
// an error there would invite a retry of something that succeeded.
func capOutput(out *OperationResult, raw []byte, capBytes int, effect contract.Effect) error {
	n := len(raw)
	if capBytes <= 0 || n <= capBytes {
		return nil
	}
	if text, ok := out.Data.(string); ok {
		out.Data = truncateText(text, capBytes) + fmt.Sprintf("\n[cerberus: output truncated — %s returned, %s shown]", sizeOf(n), sizeOf(capBytes))
		out.OutputCap = &OutputCap{Action: "truncated", Bytes: n, Cap: capBytes}
		return nil
	}
	if effect == contract.EffectRead || effect == contract.EffectReadSensitive {
		return &OutputTooLargeError{Connector: out.Connector, Operation: out.Operation, Bytes: n, Cap: capBytes}
	}
	out.Data = map[string]any{"output_withheld": fmt.Sprintf("%s ran and succeeded; its output (%s) exceeded the %s cap and is withheld", out.Operation, sizeOf(n), sizeOf(capBytes))}
	out.OutputCap = &OutputCap{Action: "withheld", Bytes: n, Cap: capBytes}
	return nil
}

// unreadableReply is the answer to a non-read whose reply was too large to
// read at all: it may have run, and whether it succeeded is unknown, which
// is said as a result rather than as an error an agent would retry.
func unreadableReply(connector, operation string) OperationResult {
	return OperationResult{Connector: connector, Operation: operation,
		Data:      map[string]any{"output_withheld": fmt.Sprintf("%s ran, but its reply was over %s and could not be read, so whether it succeeded is unknown; check the target's state before retrying. Cerberus stopped the plugin and is restarting it", operation, sizeOf(MaxMessageBytes))},
		OutputCap: &OutputCap{Action: "unreadable", Bytes: MaxMessageBytes, Cap: MaxMessageBytes}}
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func sizeOf(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return strconv.Itoa(n) + " bytes"
}

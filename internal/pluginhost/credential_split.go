package pluginhost

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Read and write credential bindings for plugins (I9). A plugin gets its
// credentials once, at load, so a split inside one process would be only
// as good as the plugin. Where connector-secrets.yaml binds a plugin's
// credentials per access, the host runs two processes instead: the plugin
// as loaded holds only its read-bound credentials and serves every read,
// and a write instance, started on the first write and holding only the
// write-bound ones, serves the writes. A read operation cannot write, however
// the plugin behaves, because its process never received a write
// credential. Which instance serves a call follows the operation's effect
// as the manifest declares it, reviewed at install.

// WithCredentialBindings gives the manager connector-secrets.yaml's
// bindings, so it can tell a plugin whose credentials are split per access.
func WithCredentialBindings(read func() (secrets.BindingFile, error)) ManagerOption {
	return func(m *Manager) { m.bindings = read }
}

// Write instances start lazily and stop when idle; a write instance that
// fails to start this often in this window is not started again until the
// plugin is loaded again.
const (
	writerIdle        = 15 * time.Minute
	writerStartWindow = 10 * time.Minute
	maxWriterStarts   = 3
)

// writerState is a split plugin's write side, on the plugin as loaded.
type writerState struct {
	mu      sync.Mutex
	current *loadedPlugin
	starts  []time.Time
}

// credentialAccess says how a plugin's credentials are bound: "" unsplit,
// or read for a split plugin's primary instance. A plugin with per-target
// bindings is refused: they are for built-ins until I9-b.
func (m *Manager) credentialAccess(id string) (secrets.Access, error) {
	if m.bindings == nil {
		return "", nil
	}
	file, err := m.bindings()
	if err != nil {
		return "", err
	}
	c := file[id]
	switch {
	case len(c.Targets) > 0:
		return "", fmt.Errorf("plugin %q not loaded: connector-secrets.yaml binds its credentials per target (targets:), which plugins do not support yet (I9-b); bind them at the connector level with read: and write: instead", id)
	case c.Split():
		return secrets.AccessRead, nil
	}
	return "", nil
}

// scoped is ctx resolving credentials for access, or unscoped for an
// unsplit plugin.
func scoped(ctx context.Context, access secrets.Access) context.Context {
	if access == "" {
		return ctx
	}
	return secrets.WithCredentialScope(ctx, secrets.CredentialScope{Access: access})
}

// instanceFor is the instance that serves a call with effect: the plugin
// as loaded, or for a split plugin's write its write instance.
func (m *Manager) instanceFor(ctx context.Context, id string, lp *loadedPlugin, effect contract.Effect, dryRun bool) (*loadedPlugin, error) {
	if lp.access == "" || secrets.AccessFor(effect, dryRun) == secrets.AccessRead {
		return lp, nil
	}
	w := lp.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if cur := w.current; cur != nil && cur.stoppedReason() == "" {
		cur.lastUse.Store(time.Now().UnixNano())
		return cur, nil
	}
	if reason := lp.stoppedReason(); reason != "" {
		return nil, &StoppedError{Connector: id, Reason: reason}
	}
	now := m.clock()
	var recent []time.Time
	for _, t := range w.starts {
		if now.Sub(t) < writerStartWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxWriterStarts {
		return nil, &StoppedError{Connector: id, Reason: fmt.Sprintf("its write instance was started %d times in %s; load the plugin again to reset it", len(recent), writerStartWindow)}
	}
	w.starts = append(recent, now)
	inst, err := m.start(scoped(context.WithoutCancel(ctx), secrets.AccessWrite), id, lp.plugin, lp.settings, secrets.AccessWrite)
	if err != nil {
		return nil, err
	}
	inst.parent = lp
	inst.lastUse.Store(time.Now().UnixNano())
	w.current = inst
	m.emit(RestartEvent{ID: id, Kind: "write_instance_started", Reason: "a write operation, with the write-bound credentials only"})
	go m.watch(id, inst)
	go m.reapIdle(id, lp, inst)
	return inst, nil
}

// reapIdle stops a write instance nobody has used for writerIdle.
func (m *Manager) reapIdle(id string, lp *loadedPlugin, inst *loadedPlugin) {
	every := time.Minute
	if idle := m.writerIdleAfter(); idle/2 < every {
		every = idle / 2
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-inst.done:
			return
		case <-t.C:
			if time.Since(time.Unix(0, inst.lastUse.Load())) >= m.writerIdleAfter() {
				m.stopWriter(id, lp, inst, "idle", false)
				return
			}
		}
	}
}

func (m *Manager) writerIdleAfter() time.Duration {
	if m.writerIdle > 0 {
		return m.writerIdle
	}
	return writerIdle
}

// stopWriter takes a write instance out of service. It is not restarted:
// the next write starts another.
func (m *Manager) stopWriter(id string, lp, inst *loadedPlugin, reason string, crashed bool) {
	if !inst.markStopped(reason) {
		return
	}
	lp.writer.mu.Lock()
	if lp.writer.current == inst {
		lp.writer.current = nil
	}
	lp.writer.mu.Unlock()
	go kill(inst.process)
	if crashed {
		m.emit(RestartEvent{ID: id, Kind: "write_instance_stopped", Reason: reason})
	}
}

// takeWriter detaches a split plugin's write instance, for the plugin
// itself stopping or unloading.
func (lp *loadedPlugin) takeWriter() *loadedPlugin {
	if lp.writer == nil {
		return nil
	}
	lp.writer.mu.Lock()
	defer lp.writer.mu.Unlock()
	w := lp.writer.current
	lp.writer.current = nil
	return w
}

// WriteInstanceRunning reports whether a split plugin's write instance is
// up, and whether the plugin is split at all.
func (m *Manager) WriteInstanceRunning(id string) (split, running bool) {
	m.mu.RLock()
	lp, ok := m.running[id]
	m.mu.RUnlock()
	if !ok || lp.access == "" {
		return false, false
	}
	lp.writer.mu.Lock()
	defer lp.writer.mu.Unlock()
	return true, lp.writer.current != nil && lp.writer.current.stoppedReason() == ""
}

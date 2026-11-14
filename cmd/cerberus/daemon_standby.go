package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// daemonLoops is the daemon's background work that standby stops: the
// resource monitor, the host monitor and its janitor, the approval sweeper,
// event export and the snapshot recorder. Each loop runs until its context
// ends. start runs every loop on a fresh context; stop cancels it and waits
// for every loop to return (joined, never abandoned). Both are idempotent.
type daemonLoops struct {
	parent context.Context
	loops  []namedLoop

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

type namedLoop struct {
	name string
	run  func(ctx context.Context)
}

func newDaemonLoops(parent context.Context) *daemonLoops {
	return &daemonLoops{parent: parent}
}

// add registers a loop. Loops are added before the first start.
func (l *daemonLoops) add(name string, run func(ctx context.Context)) {
	l.loops = append(l.loops, namedLoop{name: name, run: run})
}

// names lists the registered loops, in order.
func (l *daemonLoops) names() []string {
	out := make([]string, 0, len(l.loops))
	for _, lp := range l.loops {
		out = append(out, lp.name)
	}
	return out
}

func (l *daemonLoops) start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running || l.parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(l.parent)
	l.cancel, l.running = cancel, true
	for _, lp := range l.loops {
		l.wg.Add(1)
		go func(run func(context.Context)) {
			defer l.wg.Done()
			run(ctx)
		}(lp.run)
	}
}

func (l *daemonLoops) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.running {
		return
	}
	l.cancel()
	l.wg.Wait()
	l.running = false
}

func (l *daemonLoops) isRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// pluginSet is what standby needs of the managed plugin service.
type pluginSet interface {
	SuspendPlugins(ctx context.Context) ([]string, error)
	RestorePlugins(ctx context.Context) error
}

// sessionSet is what standby needs of the runtime for dev sessions.
type sessionSet interface {
	StopDevSessions(ctx context.Context) ([]string, error)
	StartDevSessions(ctx context.Context, ids []string) error
}

// standbyController follows the standby flag (docs/substrate-port-plan.md
// section 7.1). On entry it raises the socket fence first, then stops the
// loops, the plugins and the running dev sessions, and records the sessions
// once. On exit it starts the recorded sessions, the plugins and the loops,
// clears the list, and lowers the fence last. The first call, at daemon
// start, is an exit from a standby that never began: it starts the loops and
// plugins, and any sessions a previous standby left listed.
type standbyController struct {
	paths    standby.Paths
	fence    *cerbapi.Standby
	loops    *daemonLoops
	plugins  pluginSet
	sessions sessionSet
	logger   *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	started bool
	state   standby.State
}

func (c *standbyController) apply(ctx context.Context, st standby.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	switch {
	case st.Active && (!c.state.Active || !c.started):
		c.enter(ctx, st)
	case !st.Active && (c.state.Active || !c.started):
		c.exit(ctx)
	case st.Active:
		// The flag now names another resource (or none): only the fence changes.
		c.fence.Set(st)
		c.logger.Info("daemon.standby.resource_changed", "resource", st.Resource, "problem", st.Problem)
	}
	c.started, c.state = true, st
}

func (c *standbyController) enter(ctx context.Context, st standby.State) {
	c.fence.Set(st)
	c.loops.stop()
	stoppedPlugins, err := c.plugins.SuspendPlugins(ctx)
	if err != nil {
		c.logger.Error("daemon.standby.plugins_not_stopped", "error", err.Error())
	}
	sessions, err := c.sessions.StopDevSessions(ctx)
	if err != nil {
		c.logger.Error("daemon.standby.sessions_not_stopped", "error", err.Error())
	}
	wrote, err := c.paths.WriteSessionsOnce(sessions, c.clock())
	if err != nil {
		c.logger.Error("daemon.standby.sessions_not_recorded", "sessions", sessions, "error", err.Error())
	}
	c.logger.Info("daemon.standby.entered", "resource", st.Resource, "problem", st.Problem,
		"plugins_stopped", stoppedPlugins, "sessions_stopped", sessions, "session_list_written", wrote)
}

func (c *standbyController) exit(ctx context.Context) {
	list, ok, readErr := c.paths.ReadSessions()
	if readErr != nil {
		c.logger.Error("daemon.standby.sessions_unreadable", "error", readErr.Error(), "consequence", "no dev session is started again; start them by hand")
	}
	var startErr error
	if ok && len(list.IDs) > 0 {
		if startErr = c.sessions.StartDevSessions(ctx, list.IDs); startErr != nil {
			c.logger.Error("daemon.standby.sessions_not_started", "error", startErr.Error(), "consequence", "the session list is kept")
		}
	}
	if err := c.plugins.RestorePlugins(ctx); err != nil {
		c.logger.Error("daemon.standby.plugins_not_restored", "error", err.Error())
	}
	c.loops.start()
	// The list goes once its sessions are running again, so the next standby
	// records a fresh one; a list that could not be read or started is kept.
	if ok && readErr == nil && startErr == nil {
		if err := c.paths.ClearSessions(); err != nil {
			c.logger.Error("daemon.standby.session_list_not_cleared", "error", err.Error())
		}
	}
	c.fence.Set(standby.State{})
	if c.started {
		c.logger.Info("daemon.standby.left", "sessions_started", list.IDs)
	}
}

func (c *standbyController) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

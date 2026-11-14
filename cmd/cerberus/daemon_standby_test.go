package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// fakeLoop counts its runs and records whether each returned only after its
// context ended.
type fakeLoop struct {
	starts, returns atomic.Int64
	running         atomic.Int64
}

func (f *fakeLoop) run(ctx context.Context) {
	f.starts.Add(1)
	f.running.Add(1)
	<-ctx.Done()
	time.Sleep(5 * time.Millisecond) // a loop that takes a moment to wind down
	f.running.Add(-1)
	f.returns.Add(1)
}

// Every loop stops when the group stops, and stop returns only once each has
// returned (joined, not abandoned); every loop runs again when it starts.
func TestDaemonLoopsStopAndRestartEachLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	names := []string{"resource_monitor", "host_monitor_janitor", "host_monitor", "approval_sweeper", "event_export", "snapshot_recorder"}
	loops := newDaemonLoops(ctx)
	fakes := map[string]*fakeLoop{}
	for _, n := range names {
		f := &fakeLoop{}
		fakes[n] = f
		loops.add(n, f.run)
	}
	if strings.Join(loops.names(), ",") != strings.Join(names, ",") {
		t.Fatalf("names = %v", loops.names())
	}
	waitRunning := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for _, n := range names {
			for fakes[n].running.Load() != want {
				if time.Now().After(deadline) {
					t.Fatalf("%s running = %d, want %d", n, fakes[n].running.Load(), want)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	for round := int64(1); round <= 3; round++ {
		loops.start()
		loops.start() // idempotent
		waitRunning(1)
		if !loops.isRunning() {
			t.Fatal("not running after start")
		}
		loops.stop()
		for _, n := range names {
			if got := fakes[n].running.Load(); got != 0 {
				t.Fatalf("round %d: stop returned with %s still running", round, n)
			}
			if fakes[n].starts.Load() != round || fakes[n].returns.Load() != round {
				t.Fatalf("round %d: %s started %d, returned %d", round, n, fakes[n].starts.Load(), fakes[n].returns.Load())
			}
		}
		loops.stop() // idempotent
	}
	// After the daemon's context ends, nothing starts again.
	cancel()
	loops.start()
	if loops.isRunning() {
		t.Fatal("started after the daemon's context ended")
	}
}

// recorder notes, in order, what the controller did.
type recorder struct {
	mu      sync.Mutex
	steps   []string
	running []string
}

func (r *recorder) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, s)
}

func (r *recorder) SuspendPlugins(context.Context) ([]string, error) {
	r.note("plugins.suspend")
	return []string{"sysmon"}, nil
}

func (r *recorder) RestorePlugins(context.Context) error {
	r.note("plugins.restore")
	return nil
}

func (r *recorder) StopDevSessions(context.Context) ([]string, error) {
	r.note("sessions.stop")
	out := r.running
	r.running = nil
	return out, nil
}

func (r *recorder) StartDevSessions(_ context.Context, ids []string) error {
	r.note("sessions.start:" + strings.Join(ids, ","))
	r.running = append(r.running, ids...)
	return nil
}

func newTestController(t *testing.T, rec *recorder) (*standbyController, *cerbapi.Standby, standby.Paths) {
	t.Helper()
	paths := standby.PathsIn(t.TempDir())
	fence := cerbapi.NewStandby(nil, paths.AuditDir(), nil)
	loops := newDaemonLoops(t.Context())
	loops.add("probe", func(ctx context.Context) {
		rec.note("loop.start")
		<-ctx.Done()
		rec.note("loop.end")
	})
	t.Cleanup(loops.stop)
	return &standbyController{paths: paths, fence: fence, loops: loops, plugins: rec, sessions: rec,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, fence, paths
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.steps
	r.steps = nil
	return out
}

func waitStep(t *testing.T, rec *recorder, step string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		for _, s := range rec.steps {
			if s == step {
				rec.mu.Unlock()
				return
			}
		}
		rec.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no %s in %v", step, rec.steps)
}

// Entry raises the fence before anything stops, then stops loops, plugins and
// sessions and lists the sessions once; exit starts the listed sessions,
// plugins and loops, clears the list and lowers the fence last.
func TestStandbyControllerEntryAndExit(t *testing.T) {
	rec := &recorder{running: []string{"web", "api"}}
	c, fence, paths := newTestController(t, rec)
	ctx := t.Context()

	// Daemon start, not in standby: plugins and loops start.
	c.apply(ctx, standby.State{})
	waitStep(t, rec, "loop.start")
	if got := strings.Join(rec.take(), " "); got != "plugins.restore loop.start" {
		t.Fatalf("start = %q", got)
	}

	// Entry. The fence is up before the first thing stops.
	var fenceUpBeforeStop bool
	c.loops.loops[0].run = func(ctx context.Context) {
		rec.note("loop.start")
		<-ctx.Done()
		st, _ := fence.State()
		fenceUpBeforeStop = st.Active
		rec.note("loop.end")
	}
	c.loops.stop() // swap the probe for one that checks the fence
	c.loops.start()
	waitStep(t, rec, "loop.start")
	rec.take()
	c.apply(ctx, standby.State{Active: true, Resource: "cerberus-substrate"})
	if got := strings.Join(rec.take(), " "); got != "loop.end plugins.suspend sessions.stop" {
		t.Fatalf("entry = %q", got)
	}
	if !fenceUpBeforeStop {
		t.Fatal("the loops stopped before the fence went up")
	}
	list, ok, err := paths.ReadSessions()
	if err != nil || !ok || strings.Join(list.IDs, ",") != "web,api" {
		t.Fatalf("session list = %+v %v %v", list, ok, err)
	}
	if st, _ := fence.State(); st.Resource != "cerberus-substrate" {
		t.Fatalf("fence = %+v", st)
	}

	// The flag naming another resource moves only the fence.
	c.apply(ctx, standby.State{Active: true, Resource: "other"})
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("a resource change did %v", got)
	}
	if st, _ := fence.State(); st.Resource != "other" {
		t.Fatalf("fence = %+v", st)
	}

	// Exit.
	c.apply(ctx, standby.State{})
	waitStep(t, rec, "loop.start")
	if got := strings.Join(rec.take(), " "); got != "sessions.start:web,api plugins.restore loop.start" {
		t.Fatalf("exit = %q", got)
	}
	if _, ok, _ := paths.ReadSessions(); ok {
		t.Fatal("the session list was not cleared after its sessions started")
	}
	if st, _ := fence.State(); st.Active {
		t.Fatal("the fence is still up after exit")
	}
}

// A daemon that starts in standby starts no loop and no plugin, and a
// restart in standby (its sessions already gone) keeps the first list.
func TestStandbyControllerStartInStandbyKeepsTheFirstList(t *testing.T) {
	rec := &recorder{running: []string{"web"}}
	c, _, paths := newTestController(t, rec)
	ctx := t.Context()
	c.apply(ctx, standby.State{Active: true, Resource: "cerberus-substrate"})
	if got := strings.Join(rec.take(), " "); got != "plugins.suspend sessions.stop" {
		t.Fatalf("start in standby = %q", got)
	}
	if c.loops.isRunning() {
		t.Fatal("a loop runs in standby")
	}

	// The daemon restarts, still in standby; nothing is running now.
	rec2 := &recorder{}
	c2, _, _ := newTestController(t, rec2)
	c2.paths = paths
	c2.apply(ctx, standby.State{Active: true, Resource: "cerberus-substrate"})
	list, _, _ := paths.ReadSessions()
	if strings.Join(list.IDs, ",") != "web" {
		t.Fatalf("a restart in standby shrank the list to %v", list.IDs)
	}

	// Leaving standby starts the first list's sessions.
	c2.apply(ctx, standby.State{})
	waitStep(t, rec2, "loop.start")
	if got := rec2.take(); got[len(got)-3] != "sessions.start:web" {
		t.Fatalf("exit = %v", got)
	}
}

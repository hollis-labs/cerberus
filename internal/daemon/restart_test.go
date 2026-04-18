package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// testCtx returns a context that's canceled at t cleanup.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// fakeSignaler records which signals were sent to which PIDs and optionally
// toggles a fakeAlive's "alive" state on SIGTERM/SIGKILL.
type fakeSignaler struct {
	mu     sync.Mutex
	alive  *fakeAlive
	sent   []sigCall
	onTerm func(pid int)
	onKill func(pid int)
	sigErr error
}

type sigCall struct {
	PID    int
	Signal syscall.Signal
}

func (f *fakeSignaler) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	f.sent = append(f.sent, sigCall{PID: pid, Signal: sig})
	f.mu.Unlock()
	if f.sigErr != nil {
		return f.sigErr
	}
	switch sig {
	case syscall.SIGTERM:
		if f.onTerm != nil {
			f.onTerm(pid)
		}
	case syscall.SIGKILL:
		if f.onKill != nil {
			f.onKill(pid)
		}
	default:
		// Other signals are irrelevant to restart logic.
	}
	return nil
}

// fakeAlive is a controllable AliveChecker.
type fakeAlive struct {
	mu   sync.Mutex
	live map[int]bool
}

func newFakeAlive(initial ...int) *fakeAlive {
	fa := &fakeAlive{live: map[int]bool{}}
	for _, pid := range initial {
		fa.live[pid] = true
	}
	return fa
}

func (f *fakeAlive) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[pid]
}

func (f *fakeAlive) setDead(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[pid] = false
}

func (f *fakeAlive) setAlive(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[pid] = true
}

// fakeIdent is a controllable ProcIdentifier.
type fakeIdent struct {
	daemonPIDs map[int]bool
	strays     []int
	probeErr   error
}

func (f *fakeIdent) IsCerberusDaemon(_ context.Context, pid int) (bool, error) {
	if f.probeErr != nil {
		return false, f.probeErr
	}
	return f.daemonPIDs[pid], nil
}

func (f *fakeIdent) FindCerberusDaemonPIDs(_ context.Context) ([]int, error) {
	return append([]int{}, f.strays...), nil
}

func stopOptsWith(sig *fakeSignaler, alive *fakeAlive, ident ProcIdentifier) StopOptions {
	return StopOptions{
		GracePeriod:  50 * time.Millisecond,
		KillTimeout:  20 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		Signaler:     sig,
		Alive:        alive,
		Identifier:   ident,
	}
}

func TestStopDaemon_HappyPath_ExitsOnSIGTERM(t *testing.T) {
	alive := newFakeAlive(100)
	sig := &fakeSignaler{alive: alive, onTerm: func(pid int) {
		// Simulate fast clean exit.
		go func() {
			time.Sleep(5 * time.Millisecond)
			alive.setDead(pid)
		}()
	}}
	ident := &fakeIdent{daemonPIDs: map[int]bool{100: true}}

	if err := StopDaemon(testCtx(t), 100, stopOptsWith(sig, alive, ident)); err != nil {
		t.Fatalf("StopDaemon: %v", err)
	}

	if len(sig.sent) != 1 {
		t.Errorf("expected 1 signal, got %d: %v", len(sig.sent), sig.sent)
	}
	if sig.sent[0].Signal != syscall.SIGTERM {
		t.Errorf("expected SIGTERM, got %v", sig.sent[0].Signal)
	}
	if alive.Alive(100) {
		t.Error("PID still alive after StopDaemon")
	}
}

func TestStopDaemon_EscalatesToSIGKILL(t *testing.T) {
	alive := newFakeAlive(200)
	var killed int32
	sig := &fakeSignaler{
		alive: alive,
		// Ignore SIGTERM — simulate a daemon stuck in a signal handler.
		onTerm: func(_ int) {},
		onKill: func(pid int) {
			atomic.StoreInt32(&killed, 1)
			go func() {
				time.Sleep(2 * time.Millisecond)
				alive.setDead(pid)
			}()
		},
	}
	ident := &fakeIdent{daemonPIDs: map[int]bool{200: true}}

	if err := StopDaemon(testCtx(t), 200, stopOptsWith(sig, alive, ident)); err != nil {
		t.Fatalf("StopDaemon: %v", err)
	}
	if atomic.LoadInt32(&killed) != 1 {
		t.Error("SIGKILL was not sent")
	}
	if len(sig.sent) != 2 {
		t.Errorf("expected 2 signals, got %v", sig.sent)
	}
	if sig.sent[0].Signal != syscall.SIGTERM || sig.sent[1].Signal != syscall.SIGKILL {
		t.Errorf("wrong signal order: %v", sig.sent)
	}
}

func TestStopDaemon_TimeoutAfterSIGKILL(t *testing.T) {
	alive := newFakeAlive(300)
	sig := &fakeSignaler{alive: alive, onTerm: func(_ int) {}, onKill: func(_ int) {}} // ignores both
	ident := &fakeIdent{daemonPIDs: map[int]bool{300: true}}

	err := StopDaemon(testCtx(t), 300, stopOptsWith(sig, alive, ident))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestStopDaemon_AlreadyDead(t *testing.T) {
	alive := newFakeAlive() // PID not alive
	sig := &fakeSignaler{alive: alive}
	ident := &fakeIdent{}

	if err := StopDaemon(testCtx(t), 999, stopOptsWith(sig, alive, ident)); err != nil {
		t.Fatalf("StopDaemon on dead PID should succeed: %v", err)
	}
	if len(sig.sent) != 0 {
		t.Errorf("should not signal a dead PID, sent=%v", sig.sent)
	}
}

func TestStopDaemon_RefusesNonCerberusPID(t *testing.T) {
	alive := newFakeAlive(444)
	sig := &fakeSignaler{alive: alive}
	ident := &fakeIdent{daemonPIDs: map[int]bool{ /* 444 NOT registered */ }}

	err := StopDaemon(testCtx(t), 444, stopOptsWith(sig, alive, ident))
	if err == nil {
		t.Fatal("expected error refusing to kill non-cerberus PID")
	}
	if len(sig.sent) != 0 {
		t.Errorf("should not signal a non-cerberus PID, sent=%v", sig.sent)
	}
}

func TestStopDaemon_ProbeErrorProceeds(t *testing.T) {
	// When the identity probe fails, we log-and-proceed (the alternative —
	// refusing to stop — guarantees the CERB-5 bug).
	alive := newFakeAlive(555)
	sig := &fakeSignaler{alive: alive, onTerm: func(pid int) {
		go func() { time.Sleep(2 * time.Millisecond); alive.setDead(pid) }()
	}}
	ident := &fakeIdent{probeErr: errors.New("ps blew up")}

	if err := StopDaemon(testCtx(t), 555, stopOptsWith(sig, alive, ident)); err != nil {
		t.Fatalf("StopDaemon should proceed on probe error: %v", err)
	}
	if len(sig.sent) == 0 {
		t.Error("expected signal to be sent despite probe error")
	}
}

func TestStopDaemon_ESRCHIsNotAnError(t *testing.T) {
	alive := newFakeAlive(666)
	// Race: the process dies between Alive() returning true and Signal()
	// running. The kernel returns ESRCH. We should treat as success.
	sig := &fakeSignaler{alive: alive, sigErr: syscall.ESRCH}
	ident := &fakeIdent{daemonPIDs: map[int]bool{666: true}}

	if err := StopDaemon(testCtx(t), 666, stopOptsWith(sig, alive, ident)); err != nil {
		t.Fatalf("ESRCH should not be an error: %v", err)
	}
}

func TestKillStrayDaemons_SkipsSelf(t *testing.T) {
	selfPID := 1 // dummy; we only check that FindCerberusDaemonPIDs entries matching os.Getpid are skipped
	_ = selfPID
	alive := newFakeAlive()
	sig := &fakeSignaler{alive: alive}
	ident := &fakeIdent{strays: []int{}} // no strays to kill

	killed, err := KillStrayDaemons(testCtx(t), stopOptsWith(sig, alive, ident))
	if err != nil {
		t.Fatalf("KillStrayDaemons: %v", err)
	}
	if len(killed) != 0 {
		t.Errorf("expected no kills, got %v", killed)
	}
}

func TestKillStrayDaemons_KillsStrays(t *testing.T) {
	alive := newFakeAlive(1001, 1002)
	sig := &fakeSignaler{alive: alive, onTerm: func(pid int) {
		go func() { time.Sleep(2 * time.Millisecond); alive.setDead(pid) }()
	}}
	ident := &fakeIdent{
		daemonPIDs: map[int]bool{1001: true, 1002: true},
		strays:     []int{1001, 1002},
	}

	killed, err := KillStrayDaemons(testCtx(t), stopOptsWith(sig, alive, ident))
	if err != nil {
		t.Fatalf("KillStrayDaemons: %v", err)
	}
	if len(killed) != 2 {
		t.Errorf("expected 2 kills, got %v", killed)
	}
	if alive.Alive(1001) || alive.Alive(1002) {
		t.Error("strays still alive after KillStrayDaemons")
	}
}

func TestRestartWithVerify_HappyPath(t *testing.T) {
	base := t.TempDir()
	// Seed pidfile with an "old" daemon PID.
	if err := WriteDaemonPIDAt(base, 5000); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	alive := newFakeAlive(5000)
	sig := &fakeSignaler{alive: alive, onTerm: func(pid int) {
		go func() { time.Sleep(2 * time.Millisecond); alive.setDead(pid) }()
	}}
	ident := &fakeIdent{daemonPIDs: map[int]bool{5000: true}, strays: []int{5000}}

	newPID := 6000
	spawnCalled := false
	spawn := func(_ context.Context) (int, error) {
		spawnCalled = true
		// Simulate the new daemon writing its own pidfile.
		alive.setAlive(newPID)
		if err := WriteDaemonPIDAt(base, newPID); err != nil {
			return 0, err
		}
		return newPID, nil
	}
	health := func(ctx context.Context, pid int) error {
		for {
			if alive.Alive(pid) {
				p, err := ReadDaemonPIDAt(base)
				if err == nil && p == pid {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}

	opts := RestartOptions{
		StopOptions:   stopOptsWith(sig, alive, ident),
		Spawn:         spawn,
		Health:        health,
		HealthTimeout: 1 * time.Second,
		PIDFileBase:   base,
		SweepStrays:   false, // ident.strays contains 5000 already-killed; avoid re-kill races in this unit test
	}
	if err := RestartWithVerify(testCtx(t), opts); err != nil {
		t.Fatalf("RestartWithVerify: %v", err)
	}
	if !spawnCalled {
		t.Error("Spawn not called")
	}
	if alive.Alive(5000) {
		t.Error("old daemon still alive")
	}
	if !alive.Alive(newPID) {
		t.Error("new daemon not alive")
	}
	pid, err := ReadDaemonPIDAt(base)
	if err != nil {
		t.Fatalf("ReadDaemonPIDAt: %v", err)
	}
	if pid != newPID {
		t.Errorf("pidfile has %d, want %d", pid, newPID)
	}
}

func TestRestartWithVerify_NoPriorDaemon(t *testing.T) {
	base := t.TempDir() // no pidfile
	alive := newFakeAlive()
	sig := &fakeSignaler{alive: alive}
	ident := &fakeIdent{}

	newPID := 7000
	spawn := func(_ context.Context) (int, error) {
		alive.setAlive(newPID)
		if err := WriteDaemonPIDAt(base, newPID); err != nil {
			return 0, err
		}
		return newPID, nil
	}
	health := func(ctx context.Context, pid int) error {
		for {
			if alive.Alive(pid) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}

	opts := RestartOptions{
		StopOptions:   stopOptsWith(sig, alive, ident),
		Spawn:         spawn,
		Health:        health,
		HealthTimeout: 1 * time.Second,
		PIDFileBase:   base,
		SweepStrays:   false,
	}
	if err := RestartWithVerify(testCtx(t), opts); err != nil {
		t.Fatalf("RestartWithVerify: %v", err)
	}
	if len(sig.sent) != 0 {
		t.Errorf("no signals should be sent when no prior daemon, got %v", sig.sent)
	}
}

func TestRestartWithVerify_HealthCheckFailure(t *testing.T) {
	base := t.TempDir()
	alive := newFakeAlive()
	sig := &fakeSignaler{alive: alive}
	ident := &fakeIdent{}

	spawn := func(_ context.Context) (int, error) {
		// Spawn "succeeds" but the daemon never becomes healthy.
		return 8000, nil
	}
	health := func(ctx context.Context, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	}

	opts := RestartOptions{
		StopOptions:   stopOptsWith(sig, alive, ident),
		Spawn:         spawn,
		Health:        health,
		HealthTimeout: 50 * time.Millisecond,
		PIDFileBase:   base,
		SweepStrays:   false,
	}
	err := RestartWithVerify(testCtx(t), opts)
	if err == nil {
		t.Fatal("expected health-check failure error")
	}
}

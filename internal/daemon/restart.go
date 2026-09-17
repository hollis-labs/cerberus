package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/service"
)

// Signaler abstracts sending a signal to a PID. Production uses PosixSignaler
// which shells out to syscall.Kill; tests inject fakes.
type Signaler interface {
	// Signal sends the given signal to the PID. Returns nil on success.
	// Returning syscall.ESRCH (no such process) is expected when the process
	// has already exited.
	Signal(pid int, sig syscall.Signal) error
}

// PosixSignaler is the production Signaler.
type PosixSignaler struct{}

// Signal delivers the signal via os.Process.Signal.
func (PosixSignaler) Signal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	return proc.Signal(sig)
}

// AliveChecker reports whether a PID is alive. Separate from Signaler so
// tests can simulate "alive" without actually spawning a process.
type AliveChecker interface {
	Alive(pid int) bool
}

// PosixAliveChecker uses signal 0 to probe liveness.
type PosixAliveChecker struct{}

// Alive returns true if the PID exists.
func (PosixAliveChecker) Alive(pid int) bool {
	return daemonProcessAlive(pid)
}

// StopOptions controls the stop-then-wait sequence for a single PID.
type StopOptions struct {
	// GracePeriod is how long we wait for the process to exit after SIGTERM
	// before escalating to SIGKILL.
	GracePeriod time.Duration
	// KillTimeout is how long we wait for the process to exit after SIGKILL
	// before returning an error.
	KillTimeout time.Duration
	// PollInterval is how often we probe liveness during the wait.
	PollInterval time.Duration
	// Signaler delivers signals. Defaults to PosixSignaler.
	Signaler Signaler
	// Alive probes process liveness. Defaults to PosixAliveChecker.
	Alive AliveChecker
	// Identifier checks whether a PID is a cerberus daemon before we signal
	// it, preventing accidental kills of unrelated processes that happen to
	// share a reused PID. Defaults to PSIdentifier.
	Identifier ProcIdentifier
	// Logger receives structured events. Defaults to service.GetLogger.
	Logger *slog.Logger
}

// DefaultStopOptions returns the conventional stop timeouts.
func DefaultStopOptions() StopOptions {
	return StopOptions{
		GracePeriod:  5 * time.Second,
		KillTimeout:  1 * time.Second,
		PollInterval: 100 * time.Millisecond,
		Signaler:     PosixSignaler{},
		Alive:        PosixAliveChecker{},
		Identifier:   PSIdentifier{},
	}
}

func (o *StopOptions) fillDefaults() {
	d := DefaultStopOptions()
	if o.GracePeriod == 0 {
		o.GracePeriod = d.GracePeriod
	}
	if o.KillTimeout == 0 {
		o.KillTimeout = d.KillTimeout
	}
	if o.PollInterval == 0 {
		o.PollInterval = d.PollInterval
	}
	if o.Signaler == nil {
		o.Signaler = d.Signaler
	}
	if o.Alive == nil {
		o.Alive = d.Alive
	}
	if o.Identifier == nil {
		o.Identifier = d.Identifier
	}
	if o.Logger == nil {
		o.Logger = service.GetLogger()
	}
}

// StopDaemon sends SIGTERM to the given PID, waits up to GracePeriod for it
// to exit, and escalates to SIGKILL if necessary. Returns nil once the PID
// is verified gone. Returns an error if the PID is not a cerberus daemon
// (safety check against reused PIDs) or if it refuses to die.
//
// If the PID is already gone when we start, returns nil immediately.
func StopDaemon(ctx context.Context, pid int, opts StopOptions) error {
	opts.fillDefaults()

	if pid <= 0 {
		return nil
	}
	if !opts.Alive.Alive(pid) {
		return nil
	}

	// Identity check: make sure this PID is a cerberus daemon before we
	// kill it. Guards against PID reuse.
	ok, err := opts.Identifier.IsCerberusDaemon(ctx, pid)
	if err != nil {
		// Probe failure — log and proceed cautiously. The alternative
		// (refusing to stop) is worse: it guarantees the bug we're fixing.
		opts.Logger.Warn("daemon.restart.identity_probe_failed", "pid", pid, "error", err.Error())
	} else if !ok {
		return fmt.Errorf("PID %d is not a cerberus daemon (possible stale PID file) — refusing to kill", pid)
	}

	start := time.Now()
	opts.Logger.Info("daemon.restart.old_signaled", "pid", pid, "signal", "SIGTERM")
	if err := opts.Signaler.Signal(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}

	if waitForExit(ctx, pid, opts.GracePeriod, opts.PollInterval, opts.Alive) {
		opts.Logger.Info("daemon.restart.old_exited",
			"pid", pid,
			"signal", "SIGTERM",
			"after_ms", time.Since(start).Milliseconds())
		return nil
	}

	// Escalate.
	opts.Logger.Warn("daemon.restart.old_sigkill", "pid", pid, "grace_ms", opts.GracePeriod.Milliseconds())
	if err := opts.Signaler.Signal(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("send SIGKILL to %d: %w", pid, err)
	}
	if waitForExit(ctx, pid, opts.KillTimeout, opts.PollInterval, opts.Alive) {
		opts.Logger.Info("daemon.restart.old_exited",
			"pid", pid,
			"signal", "SIGKILL",
			"after_ms", time.Since(start).Milliseconds())
		return nil
	}

	return fmt.Errorf("timeout waiting for PID %d to exit (grace %s, kill %s)",
		pid, opts.GracePeriod, opts.KillTimeout)
}

// waitForExit polls until the PID is gone or the timeout elapses. Returns
// true if the PID exited, false if the timeout was hit or ctx was canceled.
func waitForExit(ctx context.Context, pid int, timeout, interval time.Duration, alive AliveChecker) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !alive.Alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

// KillStrayDaemons finds any cerberus-daemon processes owned by the current
// user (other than the calling process) and stops them cleanly. Returns the
// list of PIDs that were killed.
//
// This is a safety sweep used by the restart sequence to catch daemons that
// the pidfile doesn't know about — e.g. the old-daemon-and-new-daemon
// situation the CERB-5 bug produced.
func KillStrayDaemons(ctx context.Context, opts StopOptions) ([]int, error) {
	opts.fillDefaults()

	pids, err := opts.Identifier.FindCerberusDaemonPIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("find stray daemons: %w", err)
	}

	self := os.Getpid()
	var killed []int
	for _, pid := range pids {
		if pid == self {
			continue
		}
		if !opts.Alive.Alive(pid) {
			continue
		}
		// Reuse the StopDaemon sequence (identity check + TERM + wait + KILL).
		if err := StopDaemon(ctx, pid, opts); err != nil {
			opts.Logger.Warn("daemon.restart.stray_kill_failed", "pid", pid, "error", err.Error())
			continue
		}
		killed = append(killed, pid)
	}
	if len(killed) > 0 {
		opts.Logger.Info("daemon.restart.stray_killed", "pids", killed)
	}
	return killed, nil
}

// SpawnFunc starts a new daemon process and returns its PID. Production wires
// this to re-exec the cerberus binary in its daemonize-and-detach form.
type SpawnFunc func(ctx context.Context) (int, error)

// HealthFunc verifies the new daemon is responsive. Returns nil once healthy.
type HealthFunc func(ctx context.Context, pid int) error

// RestartOptions controls the full stop-then-start restart sequence.
type RestartOptions struct {
	StopOptions

	// Spawn starts the replacement daemon. Required.
	Spawn SpawnFunc
	// Health verifies the new daemon is ready. Required.
	Health HealthFunc
	// HealthTimeout is the max time we wait for the new daemon to become
	// healthy before aborting.
	HealthTimeout time.Duration
	// PIDFileBase overrides the base dir for the daemon PID file (for tests).
	// Empty means use the default ~/.
	PIDFileBase string
	// SweepStrays controls whether to run the stray-daemon sweep after
	// stopping the known daemon. Default true.
	SweepStrays bool
}

// DefaultRestartOptions returns conventional restart timeouts.
func DefaultRestartOptions() RestartOptions {
	return RestartOptions{
		StopOptions:   DefaultStopOptions(),
		HealthTimeout: 5 * time.Second,
		SweepStrays:   true,
	}
}

// RestartWithVerify performs the atomic stop-then-start restart sequence:
//  1. Read the PID from the daemon PID file.
//  2. Stop the old daemon (SIGTERM → wait → SIGKILL → wait).
//  3. Sweep for stray cerberus daemon processes and stop them too.
//  4. Spawn a new daemon.
//  5. Verify the new daemon is healthy.
//
// The caller is responsible for surfacing errors to the CLI — this function
// does not print.
func RestartWithVerify(ctx context.Context, opts RestartOptions) error {
	opts.fillDefaults()
	if opts.Spawn == nil {
		return fmt.Errorf("RestartWithVerify: Spawn is required")
	}
	if opts.Health == nil {
		return fmt.Errorf("RestartWithVerify: Health is required")
	}
	if opts.HealthTimeout == 0 {
		opts.HealthTimeout = DefaultRestartOptions().HealthTimeout
	}

	logger := opts.Logger

	// 1. Read prior PID.
	priorPID, pidErr := restartPriorPID(ctx, opts)
	if pidErr != nil && !os.IsNotExist(pidErr) {
		logger.Warn("daemon.restart.pidfile_read_failed", "error", pidErr.Error())
	}
	logger.Info("daemon.restart.began", "prior_pid", priorPID)

	// 2. Stop the old daemon.
	if priorPID > 0 && opts.Alive.Alive(priorPID) {
		if err := StopDaemon(ctx, priorPID, opts.StopOptions); err != nil {
			return fmt.Errorf("stop old daemon PID %d: %w", priorPID, err)
		}
	}

	// 3. Sweep strays.
	if opts.SweepStrays {
		if _, err := KillStrayDaemons(ctx, opts.StopOptions); err != nil {
			// Non-fatal: log and continue. The lockfile guard is the second line
			// of defense and will catch remaining strays.
			logger.Warn("daemon.restart.stray_sweep_failed", "error", err.Error())
		}
	}

	// 4. Remove the old PID file so the new daemon starts from a clean slate.
	// The new daemon will atomically write its own PID.
	if opts.PIDFileBase != "" {
		RemoveDaemonPIDAt(opts.PIDFileBase)
	} else {
		RemoveDaemonPID()
	}

	// 5. Spawn.
	newPID, err := opts.Spawn(ctx)
	if err != nil {
		return fmt.Errorf("spawn new daemon: %w", err)
	}

	// 6. Health check.
	healthStart := time.Now()
	healthCtx, cancel := context.WithTimeout(ctx, opts.HealthTimeout)
	defer cancel()
	if err := opts.Health(healthCtx, newPID); err != nil {
		return fmt.Errorf("new daemon PID %d failed health check: %w", newPID, err)
	}
	logger.Info("daemon.restart.new_ready",
		"new_pid", newPID,
		"wait_ms", time.Since(healthStart).Milliseconds())

	return nil
}

func restartPriorPID(ctx context.Context, opts RestartOptions) (int, error) {
	var (
		pid int
		err error
	)
	if opts.PIDFileBase != "" {
		pid, err = ReadDaemonPIDAt(opts.PIDFileBase)
	} else {
		pid, err = ReadDaemonPID()
	}
	if err == nil && pid > 0 {
		return pid, nil
	}

	var (
		holderPID int
		lockErr   error
	)
	if opts.PIDFileBase != "" {
		holderPID, _, lockErr = DaemonLockHolderAt(opts.PIDFileBase)
	} else {
		holderPID, _, lockErr = DaemonLockHolder()
	}
	if lockErr != nil || holderPID <= 0 {
		return 0, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, probeErr := opts.StopOptions.Identifier.IsCerberusDaemon(probeCtx, holderPID)
	if probeErr != nil || !ok {
		return 0, err
	}
	return holderPID, nil
}

// ExecutableSpawner returns a SpawnFunc that re-execs the current binary with
// the given args + env. Matches the existing fork-and-detach pattern in the
// daemon command.
//
// The spawned child is released via Process.Release so its lifetime is
// independent of the parent. The returned PID is the child's PID as reported
// by the OS — useful for health checks that need to verify "PID X is up".
func ExecutableSpawner(args []string, extraEnv []string) SpawnFunc {
	return func(ctx context.Context) (int, error) {
		exe, err := os.Executable()
		if err != nil {
			return 0, fmt.Errorf("resolve executable: %w", err)
		}
		devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0) //nolint:gosec // fixed OS-managed sink
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", os.DevNull, err)
		}
		defer func() { _ = devNull.Close() }()
		cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // exe is from os.Executable
		cmd.Env = append(os.Environ(), extraEnv...)
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		// Break terminal/process-group inheritance so the daemon child survives
		// after the parent CLI process exits.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return 0, fmt.Errorf("start: %w", err)
		}
		pid := cmd.Process.Pid
		// Detach: parent no longer owns the child.
		_ = cmd.Process.Release()
		return pid, nil
	}
}

// PIDFileHealth returns a HealthFunc that waits for the daemon to write its
// PID file (the value matching the spawned PID) and for the process to be
// alive. Used when the new daemon double-forks and reports a different PID
// than the one returned by SpawnFunc.
//
// The returned func polls at pollInterval until the context deadline.
func PIDFileHealth(base string, pollInterval time.Duration, alive AliveChecker) HealthFunc {
	if pollInterval == 0 {
		pollInterval = 100 * time.Millisecond
	}
	if alive == nil {
		alive = PosixAliveChecker{}
	}
	return func(ctx context.Context, spawnedPID int) error {
		for {
			var pid int
			var err error
			if base != "" {
				pid, err = ReadDaemonPIDAt(base)
			} else {
				pid, err = ReadDaemonPID()
			}
			if err == nil && pid > 0 && alive.Alive(pid) {
				return nil
			}
			select {
			case <-ctx.Done():
				if err != nil {
					return fmt.Errorf("pidfile not written within timeout: %w", err)
				}
				return fmt.Errorf("pidfile written but daemon PID %d not alive", pid)
			case <-time.After(pollInterval):
			}
		}
	}
}

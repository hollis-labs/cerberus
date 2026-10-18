//go:build linux

package userpresence

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// StartTerminalAgent registers polkit's terminal agent for this CLI process.
// It cannot authorize anything itself; only the daemon's separate check can.
// The caller must stop it when enrollment allowance succeeds or fails.
func StartTerminalAgent(ctx context.Context) (func(), error) {
	if err := trustedSystemFile(pkttyagentPath); err != nil {
		return nil, redact.Guidance("linux enrollment needs the system pkttyagent; %s", LinuxRecovery)
	}
	start, err := ProcessStartTime(os.Getpid())
	if err != nil {
		return nil, err
	}
	return startTerminalAgent(ctx, pkttyagentPath, fmt.Sprintf("%d,%d", os.Getpid(), start), os.Stdin, os.Stderr)
}

func startTerminalAgent(ctx context.Context, path, subject string, in, out *os.File) (func(), error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, path, "--process", subject, "--notify-fd", "3") //nolint:gosec // fixed system binary in production
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = w.Close()
		return nil, fmt.Errorf("start the polkit terminal agent: %w", err)
	}
	_ = w.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	ready := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, r); ready <- err }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		cancel()
		return nil, redact.GuidanceWrap(err, "polkit terminal agent exited before registration; %s", LinuxRecovery)
	case err := <-ready:
		if err != nil {
			stop()
			return nil, fmt.Errorf("polkit terminal agent registration: %w", err)
		}
		// A helper crash also closes the pipe. It never counts as authority:
		// the daemon still requires a successful independent polkit check.
		return stop, nil
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	case <-timer.C:
		stop()
		return nil, redact.Guidance("polkit terminal agent registration timed out; %s", LinuxRecovery)
	}
}

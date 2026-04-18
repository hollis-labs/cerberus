// Package procscan provides primitives for identifying and killing stale
// subprocesses that are still executing a binary which has since been
// replaced on disk.
//
// Motivation: when `go install` (or any equivalent build step) replaces
// $GOBIN/<name>, the old file is unlinked but any process that already
// loaded it keeps the old inode mapped. Those processes therefore keep
// executing the pre-build code indefinitely, which is invisible from the
// shell (`ls -li $GOBIN/<name>` shows the new inode) but very visible to
// callers of the stale process.
//
// procscan addresses this by:
//
//  1. Capturing a (device, inode) fingerprint of the binary BEFORE the
//     build runs (Capture).
//  2. Enumerating processes after the build and matching their executable
//     path against the captured fingerprint (PIDsByFingerprint).
//  3. Sending SIGTERM with a short grace window, then SIGKILL to any
//     survivors (KillAndWait).
//
// The expectation is that the parent of each killed subprocess (Nanite,
// Claude Code, etc.) will respawn the subprocess on its next tool call —
// at which point it picks up the freshly-installed binary.
//
// Safety guards:
//   - Only processes owned by the same uid as the caller are killed.
//   - The caller's own PID is never killed.
//   - Children of the caller's process tree are skipped (they're managed
//     by whichever subsystem spawned them; cascade-kill targets foreign
//     subprocess trees).
package procscan

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// BinaryFingerprint captures the (device, inode) of a file. Used to
// identify processes still executing the underlying binary even after
// the file is unlinked + replaced by a build step.
//
// Path is the reference path at capture time, retained for logging.
type BinaryFingerprint struct {
	Device uint64
	Inode  uint64
	Path   string
}

// IsZero reports whether the fingerprint was successfully captured. A
// zero fingerprint should be skipped by callers (PIDsByFingerprint
// against a zero fingerprint matches nothing meaningful).
func (f BinaryFingerprint) IsZero() bool {
	return f.Device == 0 && f.Inode == 0
}

// Capture returns the fingerprint of the file at path. It returns an
// error when the file does not exist, is unreadable, or path is empty.
func Capture(path string) (BinaryFingerprint, error) {
	if path == "" {
		return BinaryFingerprint{}, fmt.Errorf("procscan: empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return BinaryFingerprint{}, fmt.Errorf("procscan: stat %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return BinaryFingerprint{}, fmt.Errorf("procscan: stat %s: no Stat_t available", path)
	}
	return BinaryFingerprint{
		Device: statDev(st),
		Inode:  st.Ino,
		Path:   path,
	}, nil
}

// KillOutcome reports the result of attempting to terminate a single PID.
type KillOutcome struct {
	PID      int
	SentTerm bool
	Exited   bool
	SentKill bool
	Err      error
}

// Killer abstracts signal delivery and liveness probing so tests can
// inject deterministic fakes.
type Killer interface {
	// Signal delivers sig to pid. Returns nil on success. ESRCH (no such
	// process) is the only "expected" error and should be returned as-is.
	Signal(pid int, sig syscall.Signal) error
	// Alive reports whether pid is currently alive.
	Alive(pid int) bool
}

// PosixKiller is the production Killer.
type PosixKiller struct{}

// Signal sends sig to pid via os.Process.Signal.
func (PosixKiller) Signal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

// Alive returns true if signal 0 succeeds against pid.
func (PosixKiller) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// KillAndWait sends SIGTERM to each PID, waits up to grace for the
// process to exit, then sends SIGKILL to any survivor and waits a
// shorter timeout for that to take effect. Returns one KillOutcome per
// input PID, in input order.
//
// logger receives one rebuild.cascade.killed event per process that
// received a signal. Callers should pass a non-nil logger; nil falls
// back to slog.Default.
func KillAndWait(pids []int, grace time.Duration, logger *slog.Logger) []KillOutcome {
	return killAndWait(pids, grace, PosixKiller{}, logger)
}

// killAndWait is the testable inner loop — it accepts an injected
// Killer so unit tests can avoid spawning real processes.
func killAndWait(pids []int, grace time.Duration, k Killer, logger *slog.Logger) []KillOutcome {
	if logger == nil {
		logger = slog.Default()
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	const pollInterval = 50 * time.Millisecond
	const killWait = 500 * time.Millisecond

	out := make([]KillOutcome, len(pids))
	for i, pid := range pids {
		out[i].PID = pid
		if pid <= 0 {
			out[i].Err = fmt.Errorf("invalid pid %d", pid)
			continue
		}
		if !k.Alive(pid) {
			out[i].Exited = true
			continue
		}
		if err := k.Signal(pid, syscall.SIGTERM); err != nil {
			out[i].Err = fmt.Errorf("send SIGTERM to %d: %w", pid, err)
			continue
		}
		out[i].SentTerm = true
		logger.Info("rebuild.cascade.killed", "pid", pid, "signal", "SIGTERM")

		if waitForExit(pid, grace, pollInterval, k) {
			out[i].Exited = true
			continue
		}

		// Escalate.
		if err := k.Signal(pid, syscall.SIGKILL); err != nil {
			out[i].Err = fmt.Errorf("send SIGKILL to %d: %w", pid, err)
			continue
		}
		out[i].SentKill = true
		logger.Info("rebuild.cascade.killed", "pid", pid, "signal", "SIGKILL")
		if waitForExit(pid, killWait, pollInterval, k) {
			out[i].Exited = true
			continue
		}
		out[i].Err = fmt.Errorf("pid %d still alive after SIGKILL", pid)
	}
	return out
}

func waitForExit(pid int, total, interval time.Duration, k Killer) bool {
	deadline := time.Now().Add(total)
	for {
		if !k.Alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

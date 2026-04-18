package procscan

import (
	"log/slog"
	"os/user"
	"strconv"
	"time"
)

// pidUIDFn is a test seam; production is pidUID. Tests that need to
// inject a fake (e.g. to exercise foreign-uid filtering without a
// second-user process) replace this var and restore it on cleanup.
var pidUIDFn = pidUID

// CaptureForService resolves a service's command to its binary path and
// captures the (dev, inode) fingerprint. Returns a zero fingerprint
// (with logging) if the binary can't be resolved or stat'd — wrapper
// scripts, `go run`, missing files, etc. CascadeKillStaleSubprocesses
// is a no-op on zero fingerprints, which is the correct behavior for
// those cases.
//
// This factors the resolve→capture sequence shared by the CLI rebuild
// command and the in-process MCP rebuild handler, so both call sites
// get identical logging keys and skip semantics.
func CaptureForService(command []string, workDir string, logger *slog.Logger) BinaryFingerprint {
	if logger == nil {
		logger = slog.Default()
	}
	binary, err := ResolveCommandBinary(command, workDir)
	if err != nil {
		// ErrSkipFingerprint is the expected "interpreter command"
		// signal — log at debug, not warn, so it doesn't pollute
		// rebuild output for `go run`-wrapped services.
		logger.Debug("rebuild.cascade.resolve_failed",
			"command", command,
			"error", err.Error(),
		)
		return BinaryFingerprint{}
	}
	fp, err := Capture(binary)
	if err != nil {
		logger.Debug("rebuild.cascade.capture_failed",
			"binary", binary,
			"error", err.Error(),
		)
		return BinaryFingerprint{}
	}
	return fp
}

// CascadeKillStaleSubprocesses is the high-level entry point used by
// the rebuild path. Given a fingerprint captured BEFORE the build, it
// scans for processes still running the old (now-unlinked) inode and
// terminates them so their parents will respawn against the freshly
// installed binary.
//
// Behavior:
//   - fp.IsZero() → no-op (caller should pass a captured fingerprint;
//     a zero value is treated as "skip").
//   - logger=nil → falls back to slog.Default.
//   - All errors are logged but not returned: cascade-kill is a
//     best-effort cleanup, never a hard failure that should abort a
//     rebuild. Returns the slice of KillOutcome for caller inspection
//     and the count of stale PIDs found (may be > 0 even when no
//     outcomes were produced if all PIDs were already gone).
//
// This function is the single source of truth for the cascade-kill
// sequence — both the CLI rebuild command and the in-process RPC
// handler call it to keep behavior consistent.
func CascadeKillStaleSubprocesses(fp BinaryFingerprint, logger *slog.Logger) []KillOutcome {
	if logger == nil {
		logger = slog.Default()
	}
	if fp.IsZero() {
		return nil
	}
	pids, err := PIDsByFingerprint(fp, logger)
	if err != nil {
		logger.Warn("rebuild.cascade.scan_failed",
			"binary_path", fp.Path,
			"error", err.Error(),
		)
		return nil
	}
	if len(pids) == 0 {
		return nil
	}
	// Drop foreign-uid PIDs as a final safety belt. The platform
	// enumerator already filters out processes the caller can't
	// inspect, but on Linux /proc enumeration sees PIDs from all
	// users — we'd just fail to readlink /proc/<pid>/exe for those.
	// On darwin, proc_pidpath returns EPERM for foreign uids. So
	// matched should already be uid-filtered, but log skips for any
	// that slip through (paranoia; cheap to check).
	pids = filterByUID(pids, logger)
	if len(pids) == 0 {
		return nil
	}
	return KillAndWait(pids, 2*time.Second, logger)
}

// filterByUID drops PIDs that are not owned by the current uid. On
// every supported platform the enumerator should already filter, so
// this is a defensive double-check (logged but cheap). Returns the
// retained set in input order.
func filterByUID(pids []int, logger *slog.Logger) []int {
	self, err := user.Current()
	if err != nil {
		// Without our own uid we can't safely filter — return the
		// input unchanged and let the kernel reject any signals to
		// foreign-uid PIDs.
		return pids
	}
	selfUID, err := strconv.Atoi(self.Uid)
	if err != nil {
		return pids
	}
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		uid, err := pidUIDFn(pid)
		if err != nil {
			// Best-effort: keep the PID rather than silently dropping
			// it on a probe error. The signal will fail with EPERM
			// for foreign uids.
			out = append(out, pid)
			continue
		}
		if uid != selfUID {
			logger.Info("rebuild.cascade.skipped_foreign_uid", "pid", pid, "uid", uid)
			continue
		}
		out = append(out, pid)
	}
	return out
}

package procscan

import (
	"fmt"
	"log/slog"
	"os"
)

// PIDEnumerator lists candidate PIDs and resolves each PID to the
// fingerprint of its running executable. Production wires this to a
// platform-specific implementation (darwinEnumerator, linuxEnumerator);
// tests inject a fake.
type PIDEnumerator interface {
	// PIDs returns the set of candidate PIDs to inspect. Implementations
	// should already filter to processes the current uid can signal —
	// matching everything else is wasted work.
	PIDs() ([]int, error)
	// Fingerprint returns the (device, inode) of the executable backing
	// pid. Returns an error when the executable cannot be resolved (the
	// caller treats this as "not a match" rather than fatal).
	Fingerprint(pid int) (BinaryFingerprint, error)
}

// defaultEnumerator is set per-GOOS in procscan_darwin.go / procscan_linux.go.
var defaultEnumerator PIDEnumerator

// PIDsByFingerprint returns all PIDs owned by the same uid as the
// caller whose running executable matches fp (by Device+Inode). The
// caller's own PID is always excluded.
//
// logger receives one rebuild.cascade.stale_pid_found event per match
// and one rebuild.cascade.skipped_self event when the caller's own PID
// is a structural match. nil logger falls back to slog.Default.
//
// On enumerator failure, returns the underlying error and an empty
// slice; callers may choose to log + continue rather than abort the
// rebuild.
func PIDsByFingerprint(fp BinaryFingerprint, logger *slog.Logger) ([]int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if fp.IsZero() {
		return nil, nil
	}
	if defaultEnumerator == nil {
		return nil, fmt.Errorf("procscan: no enumerator registered for this platform")
	}
	return pidsByFingerprintWith(defaultEnumerator, fp, os.Getpid(), logger)
}

// pidsByFingerprintWith is the testable inner loop — it accepts an
// injected enumerator + self-PID so tests can avoid touching real /proc
// or libproc.
func pidsByFingerprintWith(e PIDEnumerator, fp BinaryFingerprint, selfPID int, logger *slog.Logger) ([]int, error) {
	pids, err := e.PIDs()
	if err != nil {
		return nil, fmt.Errorf("enumerate pids: %w", err)
	}
	logger.Info("rebuild.cascade.scan_began",
		"binary_path", fp.Path,
		"dev", fp.Device,
		"inode", fp.Inode,
		"candidate_pids", len(pids),
	)
	var matched []int
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		got, err := e.Fingerprint(pid)
		if err != nil {
			// PIDs come and go between enumeration and fingerprint —
			// treat any resolution failure as "not a match".
			continue
		}
		if got.Device != fp.Device || got.Inode != fp.Inode {
			continue
		}
		if pid == selfPID {
			logger.Info("rebuild.cascade.skipped_self", "pid", pid)
			continue
		}
		logger.Info("rebuild.cascade.stale_pid_found",
			"pid", pid,
			"executable", got.Path,
		)
		matched = append(matched, pid)
	}
	return matched, nil
}

package legacyexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// DaemonLockName is the old daemon's single-instance lock under its state directory
// (origin/main:internal/daemon/daemon_lock.go: ~/.cerberus/daemon.lock, an exclusive flock held
// for the daemon's life, its holder's {pid, acquired_at, origin} written inside).
const DaemonLockName = "daemon.lock"

// DaemonRunningError is a refusal because the old daemon holds its lock: it folds approvals and
// loads applied.json once at start, so a daemon running across the export would chain from a
// stale seq and rewrite applied.json from a stale map.
type DaemonRunningError struct {
	Path      string
	HolderPID int
}

func (e *DaemonRunningError) Error() string {
	who := "a process"
	if e.HolderPID > 0 {
		who = fmt.Sprintf("PID %d", e.HolderPID)
	}
	return fmt.Sprintf("the old daemon is running: %s holds its lock %s. Stop it (`$LEGACY daemon stop`, or its service) before export-legacy, and start it again in standby after", who, e.Path)
}

// probeDaemonLock tries old's daemon lock without taking it: the file is opened read-only (never
// created, written or removed), a non-blocking exclusive flock is tried and, when it is granted,
// released at once. The daemon's socket is never touched. It returns a *DaemonRunningError when
// the lock is held, and describes what it found otherwise.
func probeDaemonLock(oldHome string) (string, error) {
	path := filepath.Join(oldHome, DaemonLockName)
	f, err := os.Open(path) //nolint:gosec // the old tree's lock file, opened read-only
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", fmt.Errorf("probe the old daemon lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd()) //nolint:gosec // a file descriptor fits an int
	err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		held := &DaemonRunningError{Path: path}
		var info struct {
			PID int `json:"pid"`
		}
		if data, rerr := os.ReadFile(path); rerr == nil && json.Unmarshal(data, &info) == nil { //nolint:gosec // as above
			held.HolderPID = info.PID
		}
		return "held", held
	}
	if err != nil {
		return "", fmt.Errorf("probe the old daemon lock %s: %w", path, err)
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return "free", nil
}

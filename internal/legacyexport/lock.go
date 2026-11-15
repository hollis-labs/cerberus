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

// acquireDaemonLock takes old's daemon lock for the whole run, so the old daemon cannot start
// mid-export: the file is opened read-only (never created, written or removed) and a
// non-blocking exclusive flock is taken on it; release drops it (and the descriptor). A missing
// lock file is no lock: the run proceeds and says so ("absent"). A held lock is a
// *DaemonRunningError naming the holder. The daemon's socket is never touched.
func acquireDaemonLock(oldHome string) (status string, release func(), err error) {
	path := filepath.Join(oldHome, DaemonLockName)
	f, err := os.Open(path) //nolint:gosec // the old tree's lock file, opened read-only
	if errors.Is(err, os.ErrNotExist) {
		return "absent", func() {}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("open the old daemon lock %s: %w", path, err)
	}
	fd := int(f.Fd()) //nolint:gosec // a file descriptor fits an int
	err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		_ = f.Close()
		held := &DaemonRunningError{Path: path}
		var info struct {
			PID int `json:"pid"`
		}
		if data, rerr := os.ReadFile(path); rerr == nil && json.Unmarshal(data, &info) == nil { //nolint:gosec // as above
			held.HolderPID = info.PID
		}
		return "held", nil, held
	}
	if err != nil {
		_ = f.Close()
		return "", nil, fmt.Errorf("lock the old daemon lock %s: %w", path, err)
	}
	return "acquired", func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// daemonLockInfo is written into the daemon lock file so other processes
// can diagnose who holds it. Origin distinguishes launchd-spawned holders
// from manual ones so `daemon status` and the resource doctor can flag a
// rogue/squatter manual daemon when launchd is the intended supervisor.
type daemonLockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	Origin     string    `json:"origin,omitempty"`
}

// DaemonLockInfo is the exported lock-holder snapshot returned by
// ReadDaemonLockInfo.
type DaemonLockInfo struct {
	PID        int
	AcquiredAt time.Time
	Origin     string
}

// DaemonLock represents an acquired exclusive flock on the daemon lock file.
// flock is released automatically by the kernel on process exit, so even a
// SIGKILL'd daemon won't leak the lock.
type DaemonLock struct {
	file *os.File
	path string
}

// daemonLockPath returns ~/.cerberus/daemon.lock.
func daemonLockPath() (string, error) {
	dir, err := daemonDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// daemonLockPathAt returns a lock path rooted at a custom base (for testing).
func daemonLockPathAt(base string) (string, error) {
	dir, err := daemonDirAt(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// DaemonLockHeldError indicates the lock is already held by a live cerberus
// daemon. Callers can inspect HolderPID to report it to the user.
type DaemonLockHeldError struct {
	HolderPID int
	Path      string
}

func (e *DaemonLockHeldError) Error() string {
	return fmt.Sprintf("cerberus daemon already running (PID %d); use 'cerberus daemon restart' to replace", e.HolderPID)
}

// AcquireDaemonLock acquires an exclusive flock on ~/.cerberus/daemon.lock.
// Returns DaemonLockHeldError if the lock is held by a live cerberus daemon.
// Stale locks (holder dead, or holder alive but not a cerberus daemon) are
// taken over automatically.
//
// The lock is auto-released by the kernel on process exit (flock semantics),
// so callers don't need to worry about cleanup on crash. Calling Release()
// on normal shutdown also removes the on-disk file.
func AcquireDaemonLock() (*DaemonLock, error) {
	path, err := daemonLockPath()
	if err != nil {
		return nil, err
	}
	return acquireDaemonLockAt(path, PSIdentifier{})
}

// AcquireDaemonLockAt acquires the daemon lock in a custom base directory.
// Use for testing.
func AcquireDaemonLockAt(base string, ident ProcIdentifier) (*DaemonLock, error) {
	path, err := daemonLockPathAt(base)
	if err != nil {
		return nil, err
	}
	return acquireDaemonLockAt(path, ident)
}

// openLockFile opens or creates the lock file with 0600 perms. We don't care
// about G304 here because the path is derived from HOME + a fixed basename.
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // path is constrained to ~/.cerberus/daemon.lock
}

func acquireDaemonLockAt(path string, ident ProcIdentifier) (*DaemonLock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock file: %w", err)
	}

	// Non-blocking exclusive lock.
	if lockErr := flockExclusive(f); lockErr != nil {
		// Lock is held by another process. Determine whether the holder is a
		// live cerberus daemon (real contention) or a stale/unrelated holder.
		info, readErr := readDaemonLockInfo(path)
		holderPID := info.PID
		_ = f.Close()

		if readErr == nil && holderPID > 0 && daemonProcessAlive(holderPID) {
			// Check whether the alive holder is actually a cerberus daemon.
			// A PID reuse by an unrelated process should not block our start.
			probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			ok, _ := ident.IsCerberusDaemon(probeCtx, holderPID)
			cancel()
			if ok {
				return nil, &DaemonLockHeldError{HolderPID: holderPID, Path: path}
			}
			// Alive but not a cerberus daemon — treat as stale and take over.
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, fmt.Errorf("remove stale daemon lock: %w", rmErr)
			}
		} else {
			// Holder dead or lock file unreadable — take over.
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, fmt.Errorf("remove stale daemon lock: %w", rmErr)
			}
		}

		// Retry once after breaking the stale lock.
		f, err = openLockFile(path)
		if err != nil {
			return nil, fmt.Errorf("reopen daemon lock after break: %w", err)
		}
		if lockErr := flockExclusive(f); lockErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("acquire daemon lock after break: %w", lockErr)
		}
	}

	// Write holder info under the lock.
	info := daemonLockInfo{PID: os.Getpid(), AcquiredAt: time.Now(), Origin: DaemonOrigin()}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	data, _ := json.Marshal(info)
	if _, err := f.Write(data); err != nil {
		_ = flockRelease(f)
		_ = f.Close()
		return nil, fmt.Errorf("write daemon lock info: %w", err)
	}
	_ = f.Sync()

	return &DaemonLock{file: f, path: path}, nil
}

// flockExclusive wraps syscall.Flock with LOCK_EX|LOCK_NB. Separated so the
// integer conversion is in one place and easier to reason about.
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // fd fits in int on all supported platforms
}

// flockRelease wraps syscall.Flock with LOCK_UN.
func flockRelease(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd fits in int on all supported platforms
}

// Release releases the flock, closes the fd, and removes the lock file.
// Safe to call multiple times.
func (l *DaemonLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = flockRelease(l.file)
	err := l.file.Close()
	l.file = nil
	if l.path != "" {
		if rmErr := os.Remove(l.path); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
			err = rmErr
		}
	}
	return err
}

// DaemonLockHolder inspects the lock file and returns the holder PID + acquire
// time without acquiring the lock. Returns an error if the file doesn't exist
// or is unreadable.
func DaemonLockHolder() (pid int, since time.Time, err error) {
	path, err := daemonLockPath()
	if err != nil {
		return 0, time.Time{}, err
	}
	info, err := readDaemonLockInfo(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	return info.PID, info.AcquiredAt, nil
}

// DaemonLockHolderAt inspects lock holder info in a custom base.
func DaemonLockHolderAt(base string) (pid int, since time.Time, err error) {
	path, err := daemonLockPathAt(base)
	if err != nil {
		return 0, time.Time{}, err
	}
	info, err := readDaemonLockInfo(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	return info.PID, info.AcquiredAt, nil
}

// ReadDaemonLockInfo returns the full lock-holder snapshot including
// origin ("launchd"|"manual"). Use this when distinguishing between a
// launchd-spawned and a manual squatter daemon matters.
func ReadDaemonLockInfo() (DaemonLockInfo, error) {
	path, err := daemonLockPath()
	if err != nil {
		return DaemonLockInfo{}, err
	}
	info, err := readDaemonLockInfo(path)
	if err != nil {
		return DaemonLockInfo{}, err
	}
	return DaemonLockInfo{PID: info.PID, AcquiredAt: info.AcquiredAt, Origin: info.Origin}, nil
}

// ReadDaemonLockInfoAt is the custom-base variant of ReadDaemonLockInfo.
func ReadDaemonLockInfoAt(base string) (DaemonLockInfo, error) {
	path, err := daemonLockPathAt(base)
	if err != nil {
		return DaemonLockInfo{}, err
	}
	info, err := readDaemonLockInfo(path)
	if err != nil {
		return DaemonLockInfo{}, err
	}
	return DaemonLockInfo{PID: info.PID, AcquiredAt: info.AcquiredAt, Origin: info.Origin}, nil
}

func readDaemonLockInfo(path string) (daemonLockInfo, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to ~/.cerberus/daemon.lock
	if err != nil {
		return daemonLockInfo{}, fmt.Errorf("read daemon lock file: %w", err)
	}
	if len(data) == 0 {
		return daemonLockInfo{}, errors.New("empty daemon lock file")
	}
	var info daemonLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return daemonLockInfo{}, fmt.Errorf("parse daemon lock file: %w", err)
	}
	return info, nil
}

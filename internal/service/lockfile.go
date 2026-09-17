package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockInfo is written into the lock file so other processes can inspect who holds it.
type lockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// Lock represents an acquired file lock on a service.
type Lock struct {
	file      *os.File
	ServiceID string
	lockDir   string // override for testing; empty means use default
}

// lockDir returns the path to ~/.cerberus/locks/, creating it if needed.
func defaultLockDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".cerberus", "locks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create lock dir: %w", err)
	}
	return dir, nil
}

// lockDirAt returns a lock directory rooted at a custom base (for testing).
func lockDirAt(base string) (string, error) {
	dir := filepath.Join(base, ".cerberus", "locks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create lock dir: %w", err)
	}
	return dir, nil
}

// AcquireLock acquires a file lock for the given service. It times out after
// 5 seconds if the lock cannot be acquired. The lock file is stored at
// ~/.cerberus/locks/<serviceID>.lock.
func AcquireLock(serviceID string) (*Lock, error) {
	dir, err := defaultLockDir()
	if err != nil {
		return nil, err
	}
	return acquireLockIn(dir, serviceID)
}

// AcquireLockAt acquires a lock using a custom base directory (for testing).
func AcquireLockAt(base, serviceID string) (*Lock, error) {
	dir, err := lockDirAt(base)
	if err != nil {
		return nil, err
	}
	return acquireLockIn(dir, serviceID)
}

func acquireLockIn(dir, serviceID string) (*Lock, error) {
	path := filepath.Join(dir, serviceID+".lock")

	// Check for stale lock before attempting to acquire
	if err := breakStaleLock(path); err != nil {
		return nil, fmt.Errorf("check stale lock: %w", err)
	}

	// Check for re-entrant lock: if the same PID already holds this lock,
	// allow it by creating a new file descriptor without flock. This is safe
	// because all callers are in the same process.
	holderPID, _, readErr := readLockInfo(path)
	reentrant := readErr == nil && holderPID == os.Getpid()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if !reentrant {
		// Try to acquire with timeout
		deadline := time.Now().Add(5 * time.Second)
		for {
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				f.Close()
				// Try to read who holds the lock for a helpful error message
				pid, since, readErr := readLockInfo(path)
				if readErr == nil {
					ago := time.Since(since).Truncate(time.Second)
					return nil, fmt.Errorf("service %s is locked by PID %d (since %s ago)", serviceID, pid, ago)
				}
				return nil, fmt.Errorf("service %s is locked (timeout after 5s)", serviceID)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Write holder info into the lock file
	info := lockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now(),
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	data, _ := json.Marshal(info)
	_, _ = f.Write(data)
	_ = f.Sync()

	return &Lock{
		file:      f,
		ServiceID: serviceID,
		lockDir:   dir,
	}, nil
}

// Release releases the lock and removes the lock file.
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}

	// Unlock and close the file
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil

	// Remove the lock file
	path := filepath.Join(l.lockDir, l.ServiceID+".lock")
	os.Remove(path) // best-effort

	return err
}

// LockHolder reads the lock file to determine who holds it, without acquiring.
func LockHolder(serviceID string) (pid int, since time.Time, err error) {
	dir, err := defaultLockDir()
	if err != nil {
		return 0, time.Time{}, err
	}
	path := filepath.Join(dir, serviceID+".lock")
	return readLockInfo(path)
}

// LockHolderAt reads lock info from a custom base directory.
func LockHolderAt(base, serviceID string) (pid int, since time.Time, err error) {
	dir, err := lockDirAt(base)
	if err != nil {
		return 0, time.Time{}, err
	}
	path := filepath.Join(dir, serviceID+".lock")
	return readLockInfo(path)
}

func readLockInfo(path string) (int, time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("read lock file: %w", err)
	}
	if len(data) == 0 {
		return 0, time.Time{}, fmt.Errorf("empty lock file")
	}
	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return 0, time.Time{}, fmt.Errorf("parse lock file: %w", err)
	}
	return info.PID, info.AcquiredAt, nil
}

// breakStaleLock checks if the lock file exists and the holder PID is dead.
// If so, it removes the lock file so a new lock can be acquired.
func breakStaleLock(path string) error {
	pid, _, err := readLockInfo(path)
	if err != nil {
		// No lock file or can't read it — not stale, proceed
		return nil
	}

	if processAlive(pid) {
		// Holder is alive, lock is not stale
		return nil
	}

	// Holder is dead — break the stale lock
	return os.Remove(path)
}

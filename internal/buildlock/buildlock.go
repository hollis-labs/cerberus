// Package buildlock serializes Cerberus builds that share a source tree.
package buildlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const Filename = ".cerberus-build.lock"

type Holder struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	ResourceID string    `json:"resource_id"`
}

type ErrBuildInProgress struct {
	Holder  Holder
	LockAge time.Duration
	Path    string
}

func (e *ErrBuildInProgress) Error() string {
	return fmt.Sprintf("build in progress for source tree %s (resource %q, PID %d, age %s); retry when it finishes", filepath.Dir(e.Path), e.Holder.ResourceID, e.Holder.PID, e.LockAge.Round(time.Second))
}

// Acquire fails fast on contention. The lock file is never unlinked: removing
// it could let another caller lock a different inode while a holder is active.
// The kernel releases the flock on exit, including crashes.
func Acquire(sourceTreePath string, holder Holder) (func(), error) {
	root, err := filepath.Abs(sourceTreePath)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, Filename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // fixed basename in the trusted project source tree
	if err != nil {
		return nil, fmt.Errorf("open build lock: %w", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // OS file descriptors fit in int on supported platforms
		defer func() { _ = file.Close() }()
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("acquire build lock: %w", err)
		}
		var current Holder
		// Metadata can be briefly incomplete while the holder writes it. It is
		// diagnostic only; the kernel lock is authoritative in every case.
		_ = json.NewDecoder(file).Decode(&current)
		age := time.Duration(0)
		if !current.StartedAt.IsZero() {
			age = time.Since(current.StartedAt)
		}
		return nil, &ErrBuildInProgress{Holder: current, LockAge: age, Path: path}
	}
	holder.PID, holder.StartedAt = os.Getpid(), time.Now().UTC()
	if err = file.Truncate(0); err == nil {
		err = json.NewEncoder(file).Encode(holder)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write build lock holder: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = file.Close() }) }, nil
}

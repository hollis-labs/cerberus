//go:build !windows

package hosts

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path (created 0600) and
// returns the function that releases it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // beside the operator's config
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // fd fits an int
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd fits an int
		_ = f.Close()
	}, nil
}

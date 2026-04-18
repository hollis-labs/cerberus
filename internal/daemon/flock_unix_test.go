//go:build unix

package daemon

import (
	"os"
	"syscall"
)

func flockEx(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // fd fits in int on all supported platforms
}

func flockUn(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd fits in int on all supported platforms
}

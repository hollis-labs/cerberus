package selfexec

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// Fingerprint captures the on-disk identity of an executable file. Two
// fingerprints are equal iff inode AND mtime match. We compare both
// because some filesystems may reuse inodes after unlink, and some
// build tools may rewrite a file in place (same inode, new mtime).
type Fingerprint struct {
	Inode uint64
	MTime time.Time
	Path  string
}

// Equal reports whether two fingerprints describe the same on-disk file
// in the same revision. Path is informational and not part of equality —
// what matters is whether the bytes behind the inode have changed.
func (f Fingerprint) Equal(other Fingerprint) bool {
	return f.Inode == other.Inode && f.MTime.Equal(other.MTime)
}

// Capture stats the file at path and returns its fingerprint.
func Capture(path string) (Fingerprint, error) {
	if path == "" {
		return Fingerprint{}, fmt.Errorf("selfexec: empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("selfexec: stat %q: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Fingerprint{}, fmt.Errorf("selfexec: stat %q: unsupported FileInfo.Sys() type %T", path, info.Sys())
	}
	return Fingerprint{
		Inode: stat.Ino,
		MTime: info.ModTime(),
		Path:  path,
	}, nil
}

// CaptureSelf resolves the running executable's path and fingerprints it.
func CaptureSelf() (Fingerprint, error) {
	path, err := os.Executable()
	if err != nil {
		return Fingerprint{}, fmt.Errorf("selfexec: resolve executable path: %w", err)
	}
	return Capture(path)
}

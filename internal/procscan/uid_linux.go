//go:build linux

package procscan

import (
	"fmt"
	"os"
	"syscall"
)

// pidUID returns the owning uid of pid by stat'ing /proc/<pid>. The
// /proc/<pid> directory's owner uid is the process's real uid.
func pidUID(pid int) (int, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return 0, fmt.Errorf("stat /proc/%d: %w", pid, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, fmt.Errorf("stat /proc/%d: no Stat_t", pid)
	}
	return int(st.Uid), nil
}

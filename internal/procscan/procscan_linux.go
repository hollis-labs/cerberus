//go:build linux

package procscan

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func init() {
	defaultEnumerator = linuxEnumerator{procRoot: "/proc"}
}

// linuxEnumerator implements PIDEnumerator on Linux by reading /proc.
// procRoot is overridable for tests; production passes "/proc".
type linuxEnumerator struct {
	procRoot string
}

// PIDs lists numeric entries under /proc — those are the PIDs visible
// to the caller. Non-numeric entries (kernel state files) are skipped.
func (e linuxEnumerator) PIDs() ([]int, error) {
	entries, err := os.ReadDir(e.procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", e.procRoot, err)
	}
	pids := make([]int, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// Fingerprint reads /proc/<pid>/exe — a symlink to the running
// executable — and stats it for (device, inode). Returns an error if
// the process disappeared or we lack permission to read the symlink.
func (e linuxEnumerator) Fingerprint(pid int) (BinaryFingerprint, error) {
	if pid <= 0 {
		return BinaryFingerprint{}, fmt.Errorf("invalid pid %d", pid)
	}
	link := fmt.Sprintf("%s/%d/exe", e.procRoot, pid)
	path, err := os.Readlink(link)
	if err != nil {
		return BinaryFingerprint{}, fmt.Errorf("readlink %s: %w", link, err)
	}
	if path == "" {
		return BinaryFingerprint{}, fmt.Errorf("readlink %s: empty target", link)
	}
	info, err := os.Stat(path)
	if err != nil {
		return BinaryFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return BinaryFingerprint{}, fmt.Errorf("stat %s: no Stat_t", path)
	}
	return BinaryFingerprint{
		Device: statDev(st),
		Inode:  st.Ino,
		Path:   path,
	}, nil
}

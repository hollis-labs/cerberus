package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// daemonDir returns the path to ~/.cerberus/, creating it if needed.
func daemonDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("create cerberus dir: %w", err)
	}
	return dir, nil
}

// daemonDirAt returns a cerberus dir rooted at a custom base (for testing).
func daemonDirAt(base string) (string, error) {
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("create cerberus dir: %w", err)
	}
	return dir, nil
}

// DaemonPIDPath returns the path to ~/.cerberus/cerberus.pid.
func DaemonPIDPath() (string, error) {
	dir, err := daemonDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cerberus.pid"), nil
}

// DaemonPIDPathAt returns a daemon PID path rooted at a custom base.
func DaemonPIDPathAt(base string) (string, error) {
	dir, err := daemonDirAt(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cerberus.pid"), nil
}

// ReadDaemonPID reads the PID from the daemon PID file.
// Returns 0 and an error if the file doesn't exist or is corrupt.
func ReadDaemonPID() (int, error) {
	path, err := DaemonPIDPath()
	if err != nil {
		return 0, err
	}
	return readDaemonPIDFrom(path)
}

// ReadDaemonPIDAt reads the daemon PID from a custom base directory.
func ReadDaemonPIDAt(base string) (int, error) {
	path, err := DaemonPIDPathAt(base)
	if err != nil {
		return 0, err
	}
	return readDaemonPIDFrom(path)
}

func readDaemonPIDFrom(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("corrupt daemon PID file: %w", err)
	}
	return pid, nil
}

// WriteDaemonPID writes the current process PID to the daemon PID file atomically.
func WriteDaemonPID() error {
	path, err := DaemonPIDPath()
	if err != nil {
		return err
	}
	return writeDaemonPIDTo(path, os.Getpid())
}

// WriteDaemonPIDAt writes the given PID to a daemon PID file in a custom base.
func WriteDaemonPIDAt(base string, pid int) error {
	path, err := DaemonPIDPathAt(base)
	if err != nil {
		return err
	}
	return writeDaemonPIDTo(path, pid)
}

// writeDaemonPIDTo writes the PID atomically using a temp-then-rename pattern
// so readers never observe a half-written file.
func writeDaemonPIDTo(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		return fmt.Errorf("write daemon PID tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup; ignore error from Remove since the rename failure
		// is the actionable one.
		_ = os.Remove(tmp)
		return fmt.Errorf("rename daemon PID tmp: %w", err)
	}
	return nil
}

// RemoveDaemonPID removes the daemon PID file.
func RemoveDaemonPID() {
	path, err := DaemonPIDPath()
	if err != nil {
		return
	}
	_ = os.Remove(path) // best-effort
}

// RemoveDaemonPIDAt removes the daemon PID file in a custom base.
func RemoveDaemonPIDAt(base string) {
	path, err := DaemonPIDPathAt(base)
	if err != nil {
		return
	}
	_ = os.Remove(path) // best-effort
}

// daemonProcessAlive checks if a process with the given PID is running.
func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// CheckDaemonRunning checks if another daemon is already running.
// Returns the PID if alive, 0 if not running or stale.
// Cleans up stale PID files automatically.
func CheckDaemonRunning() (int, error) {
	pid, err := ReadDaemonPID()
	if err != nil {
		// No PID file or corrupt — not running.
		return 0, nil //nolint:nilerr // stale/missing pidfile is not an error at this layer
	}

	if daemonProcessAlive(pid) {
		return pid, nil
	}

	// Stale PID file — process is dead, clean up.
	RemoveDaemonPID()
	return 0, nil
}

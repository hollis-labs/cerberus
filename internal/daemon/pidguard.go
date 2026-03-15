package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DaemonPIDPath returns the path to ~/.cerberus/cerberus.pid.
func DaemonPIDPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create cerberus dir: %w", err)
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

// WriteDaemonPID writes the current process PID to the daemon PID file.
func WriteDaemonPID() error {
	path, err := DaemonPIDPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644)
}

// RemoveDaemonPID removes the daemon PID file.
func RemoveDaemonPID() {
	path, err := DaemonPIDPath()
	if err != nil {
		return
	}
	os.Remove(path)
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
		return 0, nil
	}

	if daemonProcessAlive(pid) {
		return pid, nil
	}

	// Stale PID file — process is dead, clean up.
	RemoveDaemonPID()
	return 0, nil
}

// KillDaemon sends SIGTERM to the running daemon process.
// Returns an error if the process cannot be signaled.
func KillDaemon(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}
	return nil
}

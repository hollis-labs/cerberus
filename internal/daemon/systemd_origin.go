package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/hollis-labs/cerberus/internal/launchenv"
)

// CanonicalDaemonUnit is the systemd user unit `cerberus install` installs
// the daemon as on Linux: the launchd label with ".service", the same name
// the cerberus-daemon-service resource derives from its service_name.
const CanonicalDaemonUnit = CanonicalDaemonServiceLabel + ".service"

// HandWrittenDaemonUnit is the name a daemon unit written by hand before
// `cerberus install` supported Linux conventionally has. Everything that
// recognizes the daemon's own unit accepts it until install replaces it.
const HandWrittenDaemonUnit = "cerberus.service"

// systemdUserUnitDir is where the user manager reads units an administrator
// did not install system-wide.
func systemdUserUnitDir(home string) string {
	return filepath.Join(home, ".config", "systemd", "user")
}

// SystemdDaemonUnitPath returns the path of the unit file for unit.
func SystemdDaemonUnitPath(unit string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(systemdUserUnitDir(home), unit), nil
}

// liveDaemonUnit is the unit the daemon is installed as: the hand-written one
// only when the canonical unit is absent and it is there.
func liveDaemonUnit(home string) string {
	dir := systemdUserUnitDir(home)
	if _, err := os.Stat(filepath.Join(dir, CanonicalDaemonUnit)); err != nil {
		if _, err := os.Stat(filepath.Join(dir, HandWrittenDaemonUnit)); err == nil {
			return HandWrittenDaemonUnit
		}
	}
	return CanonicalDaemonUnit
}

// SystemdDaemonUnit returns the unit the daemon is installed as.
func SystemdDaemonUnit() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return CanonicalDaemonUnit
	}
	return liveDaemonUnit(home)
}

// SystemdManagedDaemonUnitPath returns the path of the daemon's unit file,
// under the name it is installed as.
func SystemdManagedDaemonUnitPath() (string, error) {
	return SystemdDaemonUnitPath(SystemdDaemonUnit())
}

// SystemdManagedDaemonExists reports whether the Cerberus daemon has a
// systemd user unit on disk, under either name. A true result means systemd
// is the intended supervisor and bare `cerberus daemon` should not start a
// parallel instance. Always false off Linux.
func SystemdManagedDaemonExists() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	path, err := SystemdManagedDaemonUnitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// SystemdSpawnedSelf reports whether the current process runs as a service of
// the systemd user manager. systemd sets INVOCATION_ID on every process it
// starts, and the process's cgroup sits under user@<uid>.service; checking
// both means an INVOCATION_ID leaked into an interactive shell is not enough.
// Like XPC_SERVICE_NAME, both are inherited by the daemon's re-exec'd child.
func SystemdSpawnedSelf() bool {
	if os.Getenv("INVOCATION_ID") == "" {
		return false
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	return underUserManager(string(data), os.Getuid())
}

// underUserManager reports whether a /proc/<pid>/cgroup listing places the
// process under uid's user manager.
func underUserManager(cgroup string, uid int) bool {
	manager := "/user@" + strconv.Itoa(uid) + ".service/"
	for _, line := range strings.Split(cgroup, "\n") {
		if strings.Contains(line, manager) {
			return true
		}
	}
	return false
}

// SystemctlUser runs `systemctl --user` with the user-bus environment a
// process started outside a login session may lack, and returns its combined
// output.
func SystemctlUser(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		path = "/usr/bin/systemctl"
		if _, statErr := os.Stat(path); statErr != nil {
			return nil, fmt.Errorf("systemctl not found on PATH or in /usr/bin: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"--user"}, args...)...) //nolint:gosec // systemctl with Cerberus's own unit names
	cmd.Env = launchenv.UserBusEnviron()
	return cmd.CombinedOutput()
}

// SupervisorRestartCommand is the command an operator runs from another
// terminal to restart the supervised daemon: `launchctl kickstart -k` under
// launchd, `systemctl --user restart` under systemd.
func SupervisorRestartCommand() string {
	if runtime.GOOS == "linux" {
		return "systemctl --user restart " + SystemdDaemonUnit()
	}
	return "launchctl kickstart -k " + LaunchdServiceTarget()
}

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CanonicalDaemonServiceLabel is the launchd job label expected for the
// Cerberus daemon. Matches `service_name` on the cerberus-daemon-service
// resource.
const CanonicalDaemonServiceLabel = "com.fragments-engine.cerberus"

// CanonicalDaemonResourceID is the resource id of the Cerberus daemon in
// the v2 registry.
const CanonicalDaemonResourceID = "cerberus-daemon-service"

// LaunchdManagedDaemonPlistPath returns the canonical path of the launchd
// plist for the Cerberus daemon service.
func LaunchdManagedDaemonPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", CanonicalDaemonServiceLabel+".plist"), nil
}

// LaunchdManagedDaemonExists reports whether the canonical Cerberus daemon
// has a launchd plist on disk. A true result means launchd is the intended
// supervisor and bare `cerberus daemon` should not start a parallel
// instance.
func LaunchdManagedDaemonExists() bool {
	path, err := LaunchdManagedDaemonPlistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// LaunchdSpawnedSelf reports whether the current process was spawned by
// launchd under the canonical Cerberus daemon service label, detected via
// the XPC_SERVICE_NAME env var that launchd sets on spawn. Child processes
// re-exec'd by the daemon inherit this env var, so this also returns true
// for the foreground re-exec child.
func LaunchdSpawnedSelf() bool {
	return os.Getenv("XPC_SERVICE_NAME") == CanonicalDaemonServiceLabel
}

// DaemonOrigin returns "launchd" when the current process was spawned by
// launchd under the canonical service label, else "manual". Used to stamp
// the daemon lock file so observers can tell which entry point owns it.
func DaemonOrigin() string {
	if LaunchdSpawnedSelf() {
		return "launchd"
	}
	return "manual"
}

// LaunchdServiceTarget returns the launchctl `gui/<uid>/<label>` service
// target for the canonical Cerberus daemon service.
func LaunchdServiceTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), CanonicalDaemonServiceLabel)
}

// LaunchctlKickstart starts (or restarts, when restart=true) the
// launchd-managed daemon via `launchctl kickstart`. Returns the combined
// stdout+stderr output of the launchctl invocation so callers can surface
// failure details.
func LaunchctlKickstart(ctx context.Context, restart bool) ([]byte, error) {
	args := []string{"kickstart"}
	if restart {
		args = append(args, "-k")
	}
	args = append(args, LaunchdServiceTarget())
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput() //nolint:gosec // hardcoded launchctl call
}

// LaunchctlKill sends a SIGTERM to the launchd-managed daemon process via
// `launchctl kill SIGTERM`. Returns the combined stdout+stderr output.
func LaunchctlKill(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", "kill", "SIGTERM", LaunchdServiceTarget()).CombinedOutput() //nolint:gosec // hardcoded launchctl call
}

package main

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/hollis-labs/cerberus/internal/daemon"
	"github.com/hollis-labs/cerberus/internal/launchenv"
	"github.com/spf13/cobra"
)

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>daemon</string>
        <string>--foreground</string>
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkingDir}}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.HomeDir}}/.cerberus/logs/launchd-stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{{.HomeDir}}/.cerberus/logs/launchd-stderr.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.DaemonPath}}</string>
    </dict>
</dict>
</plist>
`

const launchdPlistName = daemon.CanonicalDaemonServiceLabel + ".plist"

// legacyLaunchdPlistName is the daemon's plist from before the label was
// renamed; install and uninstall retire it.
const legacyLaunchdPlistName = daemon.LegacyDaemonServiceLabel + ".plist"

// launchctlRun runs launchctl; a variable so a test can record the calls.
var launchctlRun = func(args ...string) error {
	return exec.Command("launchctl", args...).Run() //nolint:gosec // launchctl with Cerberus's own labels and paths
}

// retireLegacyDaemonPlist moves the daemon off the label it was installed
// under before the rename: the legacy job is booted out, so two daemons never
// contend for the socket, and its plist is removed. It reports whether there
// was one to retire.
func retireLegacyDaemonPlist(launchAgentsDir string, uid int) (bool, error) {
	legacy := filepath.Join(launchAgentsDir, legacyLaunchdPlistName)
	if !fileExists(legacy) {
		return false, nil
	}
	// Not loaded is fine: the plist is what is left to clear.
	_ = launchctlRun("bootout", fmt.Sprintf("gui/%d/%s", uid, daemon.LegacyDaemonServiceLabel))
	if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		return true, fmt.Errorf("removing the launch agent from before the rename (%s): %w", legacy, err)
	}
	return true, nil
}

type launchdData struct {
	Label      string
	BinaryPath string
	WorkingDir string
	HomeDir    string
	DaemonPath string
}

// launchdBasePath is the PATH launchd hands a user agent that declares no
// EnvironmentVariables of its own. Every entry is kept as the tail of the
// composed PATH so the daemon can still find the system tools even if the
// installing user's PATH is odd.
const launchdBasePath = launchenv.LaunchdBasePath

// daemonLaunchPath composes the PATH baked into the daemon's launchd job.
//
// Without an EnvironmentVariables key launchd gives the job only
// launchdBasePath, and the daemon shells out: `go` for a resource build,
// `docker` for the Docker connector. Neither lives in those four directories
// on any normal Mac, which is how the Docker connector went silently dead —
// the general case is that every `cerberus install` produced a daemon that
// could not find its own toolchain.
//
// The PATH is composed from the installing user's environment rather than
// hardcoded: /opt/homebrew is wrong on an Intel Mac, /usr/local/bin is wrong
// under MacPorts, and neither is right for a toolchain in ~/.local/bin or
// $GOBIN. Whatever resolved `cerberus install` is what the daemon gets.
//
// Only PATH is carried over. Secrets deliberately do not travel in the
// environment (see AGENTS.md), and copying the installing shell's whole
// environment into a persistent launchd job would do exactly that.
//
// Entries are filtered to absolute paths (launchenv.Path says why).
func daemonLaunchPath(envPath string) string {
	return launchenv.Path(envPath, launchdBasePath)
}

// resolveDaemonBinaryPath returns the absolute path of the cerberus binary that
// should be baked into the launchd plist. We use the path of the currently
// running executable (symlink-resolved) so the plist always points at whichever
// cerberus the user just invoked — Homebrew, /usr/local/bin, ~/.local/bin,
// $GOBIN, or a source-build location. This avoids the "I `brew install`d but
// my plist still points at a stale ~/.cerberus/bin/" footgun.
func resolveDaemonBinaryPath(executable func() (string, error)) (string, error) {
	exePath, err := executable()
	if err != nil {
		return "", fmt.Errorf("could not determine binary path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		return resolved, nil
	}
	return exePath, nil
}

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the cerberus daemon as a launch agent (macOS) or systemd user unit (Linux)",
	Long:  "Bootstraps or repairs the service that runs the Cerberus daemon. On macOS, the launch agent for the daemon label (`com.hollis-labs.cerberus`); a daemon installed under the label it had before the rename (`com.fragments-engine.cerberus`) is booted out and its plist removed first. On Linux, the systemd user unit `com.hollis-labs.cerberus.service` in ~/.config/systemd/user; a hand-written `cerberus.service` that runs `cerberus daemon` is disabled and removed first, so two daemons never run, and install says how to enable lingering if it is off. The canonical ongoing management path is now the v2 `cerberus-daemon-service` resource; this command remains as a low-level bootstrap and recovery helper when the daemon is not yet available over the socket.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "linux" {
			installer, err := newSystemdInstaller(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return installer.install(cmd.Context())
		}
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("install is currently supported on macOS and Linux only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		binPath, err := resolveDaemonBinaryPath(os.Executable)
		if err != nil {
			return err
		}

		workDir := home

		launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(launchAgentsDir, 0755); err != nil { //nolint:gosec,govet
			return fmt.Errorf("creating launch agents directory: %w", err)
		}

		// Ensure logs directory exists
		logsDir := filepath.Join(home, ".cerberus", "logs")
		if err := os.MkdirAll(logsDir, 0755); err != nil { //nolint:gosec,govet
			return fmt.Errorf("creating logs directory: %w", err)
		}

		// Render the plist template
		tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
		if err != nil {
			return fmt.Errorf("parsing plist template: %w", err)
		}

		plistPath := filepath.Join(launchAgentsDir, launchdPlistName)

		// Retire the daemon's job from before the label rename first.
		retired, retireErr := retireLegacyDaemonPlist(launchAgentsDir, os.Getuid())
		if retireErr != nil {
			return retireErr
		}
		if retired {
			fmt.Printf("Retired the launch agent from before the rename: %s\n", filepath.Join(launchAgentsDir, legacyLaunchdPlistName))
		}

		// Unload existing agent if present
		if _, err := os.Stat(plistPath); err == nil { //nolint:govet
			exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck,gosec
		}

		f, err := os.Create(plistPath) //nolint:gosec
		if err != nil {
			return fmt.Errorf("creating plist: %w", err)
		}

		data := launchdData{
			Label:      daemon.CanonicalDaemonServiceLabel,
			BinaryPath: binPath,
			WorkingDir: workDir,
			HomeDir:    home,
			DaemonPath: html.EscapeString(daemonLaunchPath(os.Getenv("PATH"))),
		}
		if err := tmpl.Execute(f, data); err != nil {
			f.Close() //nolint:errcheck
			return fmt.Errorf("writing plist: %w", err)
		}
		f.Close() //nolint:errcheck

		// Load the agent
		if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil { //nolint:gosec
			return fmt.Errorf("launchctl load failed: %s: %w", strings.TrimSpace(string(out)), err)
		}

		fmt.Printf("Installed launch agent: %s\n", plistPath)
		fmt.Printf("Binary: %s\n", binPath)
		fmt.Println("Cerberus daemon will start automatically on login.")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the cerberus daemon launch agent (macOS) or systemd user unit (Linux)",
	Long:  "Unloads and removes the service that runs the Cerberus daemon. On macOS, the launch agent for the daemon label (`com.hollis-labs.cerberus`), and the one from before the rename (`com.fragments-engine.cerberus`) if it is still there. On Linux, the systemd user unit `com.hollis-labs.cerberus.service`, and a hand-written `cerberus.service` that runs `cerberus daemon` if it is still there. If `cerberus-daemon-service` is present in the v2 resource lane, prefer `cerberus resource remove cerberus-daemon-service --ack` for normal lifecycle management.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "linux" {
			installer, err := newSystemdInstaller(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return installer.uninstall(cmd.Context())
		}
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("uninstall is currently supported on macOS and Linux only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
		plistPath := filepath.Join(launchAgentsDir, launchdPlistName)

		retired, err := retireLegacyDaemonPlist(launchAgentsDir, os.Getuid())
		if err != nil {
			return err
		}
		if retired {
			fmt.Printf("Removed launch agent: %s\n", filepath.Join(launchAgentsDir, legacyLaunchdPlistName))
		}
		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			if !retired {
				fmt.Println("Launch agent not installed, nothing to do.")
			}
			return nil
		}

		// Unload the agent
		exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck,gosec

		// Remove the plist
		if err := os.Remove(plistPath); err != nil {
			return fmt.Errorf("removing plist: %w", err)
		}

		fmt.Printf("Removed launch agent: %s\n", plistPath)
		fmt.Println("Cerberus daemon will no longer start automatically.")
		return nil
	},
}

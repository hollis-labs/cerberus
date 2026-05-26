package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.fragments-engine.cerberus</string>
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
</dict>
</plist>
`

const launchdPlistName = "com.fragments-engine.cerberus.plist"

type launchdData struct {
	BinaryPath string
	WorkingDir string
	HomeDir    string
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
	Short: "Install the cerberus daemon launch agent",
	Long:  "Bootstraps or repairs the macOS launch agent for the Cerberus daemon label (`com.fragments-engine.cerberus`). The canonical ongoing management path is now the v2 `cerberus-daemon-service` resource; this command remains as a low-level bootstrap and recovery helper when the daemon is not yet available over the socket.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("install is currently supported on macOS only")
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

		// Unload existing agent if present
		if _, err := os.Stat(plistPath); err == nil { //nolint:govet
			exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck,gosec
		}

		f, err := os.Create(plistPath) //nolint:gosec
		if err != nil {
			return fmt.Errorf("creating plist: %w", err)
		}

		data := launchdData{
			BinaryPath: binPath,
			WorkingDir: workDir,
			HomeDir:    home,
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
	Short: "Remove the cerberus daemon launch agent",
	Long:  "Unloads and removes the macOS launch agent for the Cerberus daemon label (`com.fragments-engine.cerberus`). If `cerberus-daemon-service` is present in the v2 resource lane, prefer `cerberus resource remove cerberus-daemon-service` for normal lifecycle management.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("uninstall is currently supported on macOS only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdPlistName)

		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			fmt.Println("Launch agent not installed, nothing to do.")
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

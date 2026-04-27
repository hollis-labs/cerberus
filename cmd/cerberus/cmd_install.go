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

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the cerberus daemon launch agent",
	Long:  "Installs the macOS launch agent for the Cerberus daemon itself so daemon-backed CLI, socket API, and MCP flows are available on login. This is separate from v2 os_service resource management.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("install is currently supported on macOS only")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not determine home directory: %w", err)
		}

		// Prefer GOBIN / GOPATH/bin so launchd always uses the go-installed
		// binary. This avoids stale-binary issues when agents build locally
		// (go build -o ./cerberus) but the PATH resolves to ~/go/bin.
		binPath := filepath.Join(home, "go", "bin", "cerberus")
		if gobin := os.Getenv("GOBIN"); gobin != "" {
			binPath = filepath.Join(gobin, "cerberus")
		} else if gopath := os.Getenv("GOPATH"); gopath != "" {
			binPath = filepath.Join(gopath, "bin", "cerberus")
		}
		if _, statErr := os.Stat(binPath); statErr != nil {
			// Fallback: use whichever binary is running right now
			exePath, exeErr := os.Executable()
			if exeErr != nil {
				return fmt.Errorf("could not determine binary path: %w", exeErr)
			}
			binPath, _ = filepath.EvalSymlinks(exePath)
		}

		workDir := home

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

		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdPlistName)

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
	Long:  "Unloads and removes the macOS launch agent for the Cerberus daemon itself. This does not remove any v2 os_service resources.",
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

package pluginhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// TransportFactory turns a launched subprocess command into a protocol-level
// Process. The plugin-sdk JSON-RPC transport will implement this boundary.
type TransportFactory interface {
	Start(ctx context.Context, cmd *exec.Cmd) (Process, error)
}

// SubprocessLauncher resolves a validated plugin entrypoint into an exec.Cmd
// without using shell parsing or ambient PATH lookup.
type SubprocessLauncher struct {
	Transport TransportFactory
	Env       []string
}

var _ Launcher = SubprocessLauncher{}

func (l SubprocessLauncher) Launch(ctx context.Context, plugin InstalledPlugin) (Process, error) {
	if l.Transport == nil {
		return nil, fmt.Errorf("plugin transport is not configured")
	}
	if err := plugin.Spec.Validate(plugin.Path); err != nil {
		return nil, err
	}

	commandPath, err := ResolveEntrypoint(plugin.Path, plugin.Spec.Entrypoint)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, commandPath, plugin.Spec.Entrypoint.Args...)
	cmd.Dir = plugin.Path

	// The base environment carries no credential handle. Anything that does is
	// unlocked by a capability the plugin declared and the host granted, so a
	// plugin that asked for nothing is launched with nothing extra.
	env := append([]string{}, l.Env...)
	env = append(env, CapabilityEnv(plugin.Granted)...)
	cmd.Env = env
	return l.Transport.Start(ctx, cmd)
}

func ResolveEntrypoint(pluginDir string, entrypoint Entrypoint) (string, error) {
	if pluginDir == "" {
		return "", fmt.Errorf("plugin directory is required")
	}
	if err := entrypoint.Validate(pluginDir); err != nil {
		return "", err
	}

	baseDir, err := filepath.Abs(pluginDir)
	if err != nil {
		return "", fmt.Errorf("resolve plugin dir: %w", err)
	}
	commandPath := filepath.Join(baseDir, filepath.Clean(entrypoint.Command))
	commandPath, err = filepath.Abs(commandPath)
	if err != nil {
		return "", fmt.Errorf("resolve entrypoint: %w", err)
	}
	if !pathAllowed(commandPath, []string{baseDir}) {
		return "", fmt.Errorf("entrypoint command must stay inside the plugin directory")
	}

	info, err := os.Stat(commandPath)
	if err != nil {
		return "", fmt.Errorf("stat entrypoint command: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("entrypoint command %q is a directory", entrypoint.Command)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("entrypoint command %q is not executable", entrypoint.Command)
	}
	return commandPath, nil
}

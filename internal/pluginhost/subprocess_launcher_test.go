package pluginhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTransportFactory struct {
	cmd *exec.Cmd
}

func (f *fakeTransportFactory) Start(_ context.Context, cmd *exec.Cmd) (Process, error) {
	f.cmd = cmd
	return &fakeProcess{}, nil
}

func writeExecutable(t *testing.T, dir, rel string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func testPluginSpec(entrypoint Entrypoint) PluginYAML {
	return PluginYAML{
		SchemaVersion: "1",
		ID:            "docker",
		Version:       "dev",
		Protocol:      "plugin-sdk/subprocess",
		Runtime:       "subprocess",
		Entrypoint:    entrypoint,
		Cerberus: CerberusPluginBlock{
			Connector: validManifest(),
		},
	}
}

func TestResolveEntrypointReturnsAbsoluteExecutablePath(t *testing.T) {
	pluginDir := t.TempDir()
	want := writeExecutable(t, pluginDir, "bin/docker-plugin")

	got, err := ResolveEntrypoint(pluginDir, Entrypoint{Command: "bin/docker-plugin"})
	if err != nil {
		t.Fatalf("ResolveEntrypoint: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveEntrypoint = %q, want %q", got, want)
	}
}

func TestResolveEntrypointRejectsNonExecutableCommand(t *testing.T) {
	pluginDir := t.TempDir()
	path := filepath.Join(pluginDir, "bin/docker-plugin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ResolveEntrypoint(pluginDir, Entrypoint{Command: "bin/docker-plugin"})
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("ResolveEntrypoint error = %v, want executable validation", err)
	}
}

func TestSubprocessLauncherUsesExplicitEnvAndResolvedPath(t *testing.T) {
	pluginDir := t.TempDir()
	commandPath := writeExecutable(t, pluginDir, "bin/docker-plugin")
	transport := &fakeTransportFactory{}
	launcher := SubprocessLauncher{
		Transport: transport,
		Env:       []string{"CERBERUS_ENV=test", "HOME=/nonexistent"},
	}
	plugin := InstalledPlugin{
		ID:       "docker",
		Version:  "dev",
		Path:     pluginDir,
		Origin:   OriginInstalled,
		Spec:     testPluginSpec(Entrypoint{Command: "bin/docker-plugin", Args: []string{"--serve"}}),
		Manifest: validManifest(),
	}

	if _, err := launcher.Launch(context.Background(), plugin); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if transport.cmd == nil {
		t.Fatal("expected transport command")
	}
	if transport.cmd.Path != commandPath {
		t.Fatalf("cmd.Path = %q, want %q", transport.cmd.Path, commandPath)
	}
	if transport.cmd.Dir != pluginDir {
		t.Fatalf("cmd.Dir = %q, want %q", transport.cmd.Dir, pluginDir)
	}
	if len(transport.cmd.Env) != 2 || transport.cmd.Env[0] != "CERBERUS_ENV=test" || transport.cmd.Env[1] != "HOME=/nonexistent" {
		t.Fatalf("cmd.Env = %#v", transport.cmd.Env)
	}
	if len(transport.cmd.Args) != 2 || transport.cmd.Args[1] != "--serve" {
		t.Fatalf("cmd.Args = %#v", transport.cmd.Args)
	}
}

func TestSubprocessLauncherRejectsMissingTransport(t *testing.T) {
	pluginDir := t.TempDir()
	writeExecutable(t, pluginDir, "bin/docker-plugin")
	launcher := SubprocessLauncher{}
	plugin := InstalledPlugin{
		ID:       "docker",
		Version:  "dev",
		Path:     pluginDir,
		Origin:   OriginInstalled,
		Spec:     testPluginSpec(Entrypoint{Command: "bin/docker-plugin"}),
		Manifest: validManifest(),
	}

	_, err := launcher.Launch(context.Background(), plugin)
	if err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("Launch error = %v, want transport error", err)
	}
}

// The launcher must compose the base environment with whatever the grant
// unlocks. Testing CapabilityEnv alone would pass even if the launcher never
// called it, which is the bug that would put SSH_AUTH_SOCK back in front of
// every plugin.
func TestSubprocessLauncherAddsOnlyGrantedCapabilityEnv(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")

	newPlugin := func(dir string, granted []string) InstalledPlugin {
		return InstalledPlugin{
			ID:       "example",
			Version:  "dev",
			Path:     dir,
			Origin:   OriginInstalled,
			Spec:     testPluginSpec(Entrypoint{Command: "bin/example-plugin"}),
			Manifest: validManifest(),
			Granted:  granted,
		}
	}

	t.Run("no grant means no credential handle", func(t *testing.T) {
		dir := t.TempDir()
		writeExecutable(t, dir, "bin/example-plugin")
		transport := &fakeTransportFactory{}
		launcher := SubprocessLauncher{Transport: transport, Env: []string{"PATH=/usr/bin"}}

		if _, err := launcher.Launch(context.Background(), newPlugin(dir, nil)); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		for _, entry := range transport.cmd.Env {
			if strings.HasPrefix(entry, "SSH_AUTH_SOCK=") || strings.HasPrefix(entry, "DOCKER_HOST=") {
				t.Fatalf("ungranted plugin was launched with %q", entry)
			}
		}
	})

	t.Run("grant unlocks exactly its own variables", func(t *testing.T) {
		dir := t.TempDir()
		writeExecutable(t, dir, "bin/example-plugin")
		transport := &fakeTransportFactory{}
		launcher := SubprocessLauncher{Transport: transport, Env: []string{"PATH=/usr/bin"}}

		plugin := newPlugin(dir, []string{CapabilitySSHAgent})
		if _, err := launcher.Launch(context.Background(), plugin); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		joined := strings.Join(transport.cmd.Env, " ")
		if !strings.Contains(joined, "PATH=/usr/bin") {
			t.Errorf("base environment was lost: %v", transport.cmd.Env)
		}
		if !strings.Contains(joined, "SSH_AUTH_SOCK=/tmp/agent.sock") {
			t.Errorf("granted capability did not reach the process: %v", transport.cmd.Env)
		}
		if strings.Contains(joined, "DOCKER_HOST=") {
			t.Errorf("an ungranted capability's variable leaked: %v", transport.cmd.Env)
		}
	})
}

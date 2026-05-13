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
		Trust:    TrustDecision{Tier: TrustTierSigned},
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
		Trust:    TrustDecision{Tier: TrustTierSigned},
		Spec:     testPluginSpec(Entrypoint{Command: "bin/docker-plugin"}),
		Manifest: validManifest(),
	}

	_, err := launcher.Launch(context.Background(), plugin)
	if err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("Launch error = %v, want transport error", err)
	}
}

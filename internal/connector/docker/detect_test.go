package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDetectDockerHonorsPathOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeExecutable(t, dir, "docker", 0o755)
	t.Setenv(DockerPathEnv, path)

	got, ok := DetectDocker()
	if !ok {
		t.Fatal("DetectDocker returned not-found for a valid override")
	}
	if got != path {
		t.Fatalf("DetectDocker = %q, want the override %q", got, path)
	}
}

// A bad override must fail rather than silently falling through to PATH: an
// operator who names a docker binary explicitly wants that one, and quietly
// running a different one is worse than an error.
func TestDetectDockerRejectsMissingOverride(t *testing.T) {
	t.Setenv(DockerPathEnv, filepath.Join(t.TempDir(), "nope"))

	if _, ok := DetectDocker(); ok {
		t.Fatal("DetectDocker accepted an override that does not exist")
	}
}

func TestDetectDockerRejectsNonExecutableOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DockerPathEnv, writeExecutable(t, dir, "docker", 0o644))

	if _, ok := DetectDocker(); ok {
		t.Fatal("DetectDocker accepted a non-executable override")
	}
}

// The reason the fallbacks exist: launchd hands the daemon a PATH that does not
// include /usr/local/bin, so PATH lookup alone reported "docker not found" on a
// machine where docker worked fine in any shell.
func TestDetectDockerFindsBinaryWithEmptyPATH(t *testing.T) {
	var installed string
	for _, candidate := range fallbackDockerPaths {
		if executableFile(candidate) {
			installed = candidate
			break
		}
	}
	if installed == "" {
		t.Skip("no docker binary in a known install location on this machine")
	}

	t.Setenv("PATH", "")
	got, ok := DetectDocker()
	if !ok {
		t.Fatal("DetectDocker failed with an empty PATH; fallback locations did not apply")
	}
	if got != installed {
		t.Fatalf("DetectDocker = %q, want %q", got, installed)
	}
}

func TestExecutableFileRejectsDirectory(t *testing.T) {
	if executableFile(t.TempDir()) {
		t.Fatal("executableFile accepted a directory")
	}
}

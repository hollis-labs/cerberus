package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDaemonBinaryPathReturnsExecutablePath(t *testing.T) {
	got, err := resolveDaemonBinaryPath(func() (string, error) {
		return "/usr/local/bin/cerberus", nil
	})
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	if got != "/usr/local/bin/cerberus" {
		t.Fatalf("expected /usr/local/bin/cerberus, got %q", got)
	}
}

func TestResolveDaemonBinaryPathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "real-cerberus")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	linkBin := filepath.Join(dir, "cerberus")
	if err := os.Symlink(realBin, linkBin); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := resolveDaemonBinaryPath(func() (string, error) {
		return linkBin, nil
	})
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	// EvalSymlinks also resolves any symlinks in the parent path, so resolve
	// the expected real binary the same way before comparing.
	wantResolved, err := filepath.EvalSymlinks(realBin)
	if err != nil {
		t.Fatalf("eval symlinks on target: %v", err)
	}
	if got != wantResolved {
		t.Fatalf("expected symlink-resolved path %q, got %q", wantResolved, got)
	}
}

func TestResolveDaemonBinaryPathPropagatesExecutableError(t *testing.T) {
	_, err := resolveDaemonBinaryPath(func() (string, error) {
		return "", errors.New("boom")
	})
	if err == nil {
		t.Fatalf("expected error from executable() to propagate")
	}
}

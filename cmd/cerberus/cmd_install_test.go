package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveDaemonBinaryPathPrefersReleaseBinary(t *testing.T) {
	home := t.TempDir()
	releasePath := filepath.Join(home, releaseBinaryRelativePath)
	goPathBin := filepath.Join(home, "go", "bin", "cerberus")

	got, err := resolveDaemonBinaryPath(home,
		func() (string, error) { return "/running/cerberus", nil },
		func(path string) error {
			switch path {
			case releasePath, goPathBin:
				return nil
			default:
				return errors.New("missing")
			}
		},
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	if got != releasePath {
		t.Fatalf("expected release binary %q, got %q", releasePath, got)
	}
}

func TestResolveDaemonBinaryPathFallsBackToGoInstall(t *testing.T) {
	home := t.TempDir()
	goPathBin := filepath.Join(home, "go", "bin", "cerberus")

	got, err := resolveDaemonBinaryPath(home,
		func() (string, error) { return "/running/cerberus", nil },
		func(path string) error {
			if path == goPathBin {
				return nil
			}
			return errors.New("missing")
		},
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	if got != goPathBin {
		t.Fatalf("expected go install binary %q, got %q", goPathBin, got)
	}
}

func TestResolveDaemonBinaryPathHonorsGOBINAfterReleasePath(t *testing.T) {
	home := t.TempDir()
	gobin := filepath.Join(home, "custom-bin")
	gobinPath := filepath.Join(gobin, "cerberus")

	got, err := resolveDaemonBinaryPath(home,
		func() (string, error) { return "/running/cerberus", nil },
		func(path string) error {
			if path == gobinPath {
				return nil
			}
			return errors.New("missing")
		},
		func(key string) string {
			if key == "GOBIN" {
				return gobin
			}
			return ""
		},
	)
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	if got != gobinPath {
		t.Fatalf("expected GOBIN binary %q, got %q", gobinPath, got)
	}
}

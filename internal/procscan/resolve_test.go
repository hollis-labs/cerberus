package procscan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCommandBinary_AbsolutePath(t *testing.T) {
	t.Parallel()
	got, err := ResolveCommandBinary([]string{"/usr/bin/foo", "arg"}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "/usr/bin/foo" {
		t.Errorf("got %q, want /usr/bin/foo", got)
	}
}

func TestResolveCommandBinary_RelativeWithDir(t *testing.T) {
	t.Parallel()
	got, err := ResolveCommandBinary([]string{"./scripts/wrapper.sh"}, "/home/user/proj")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "/home/user/proj/scripts/wrapper.sh"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveCommandBinary_BareNameViaPath(t *testing.T) {
	t.Parallel()
	// Stage a temp binary and put its dir on PATH.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fakecmd")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write: %v", err)
	}
	old := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", old) })
	_ = os.Setenv("PATH", dir+":"+old)

	got, err := ResolveCommandBinary([]string{"fakecmd"}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != binPath {
		t.Errorf("got %q, want %q", got, binPath)
	}
}

func TestResolveCommandBinary_Empty(t *testing.T) {
	t.Parallel()
	if _, err := ResolveCommandBinary(nil, ""); err == nil {
		t.Error("expected error for nil command")
	}
	if _, err := ResolveCommandBinary([]string{""}, ""); err == nil {
		t.Error("expected error for empty command[0]")
	}
}

func TestResolveCommandBinary_InterpretersSkipped(t *testing.T) {
	t.Parallel()
	for _, bin := range []string{"go", "sh", "bash", "/usr/bin/python3", "node"} {
		_, err := ResolveCommandBinary([]string{bin, "arg"}, "")
		if !errors.Is(err, ErrSkipFingerprint) {
			t.Errorf("%s: expected ErrSkipFingerprint, got %v", bin, err)
		}
	}
}

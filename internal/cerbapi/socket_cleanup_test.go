package cerbapi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSocketCleanupRemovesDanglingLinkAndToleratesMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	if err := os.Symlink(path+"-missing", path); err != nil {
		t.Fatal(err)
	}
	s := NewSocketServer(nil, path)
	if err := s.cleanupSocket(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket link still exists: %v", err)
	}
	if err := s.cleanupSocket(); err != nil {
		t.Fatalf("missing socket is harmless: %v", err)
	}
}

func TestSocketCleanupSurfacesRemovalFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewSocketServer(nil, filepath.Join(parent, "socket"))
	if err := s.cleanupSocket(); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("expected path error, got %v", err)
	}
}

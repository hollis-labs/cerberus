package selfexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapture_StableForUnchangedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "binary")
	writeFile(t, path, []byte("v1"))

	first, err := Capture(path)
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	second, err := Capture(path)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if !first.Equal(second) {
		t.Fatalf("expected fingerprints to match for unchanged file: %+v vs %+v", first, second)
	}
}

func TestCapture_DetectsMtimeChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "binary")
	writeFile(t, path, []byte("v1"))

	before, err := Capture(path)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	// Bump mtime forward so even FS that round to seconds will detect.
	future := time.Now().Add(2 * time.Second)
	if chErr := os.Chtimes(path, future, future); chErr != nil {
		t.Fatalf("chtimes: %v", chErr)
	}

	after, err := Capture(path)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before.Equal(after) {
		t.Fatalf("expected mtime change to break equality: %+v == %+v", before, after)
	}
}

func TestCapture_DetectsInodeChangeViaRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "binary")
	writeFile(t, path, []byte("v1"))

	before, err := Capture(path)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	// Stage a replacement file with different bytes, then atomically
	// rename it over the target. This typically yields a new inode.
	staging := filepath.Join(dir, "staging")
	writeFile(t, staging, []byte("v2-much-longer-content"))
	if rnErr := os.Rename(staging, path); rnErr != nil {
		t.Fatalf("rename: %v", rnErr)
	}

	after, err := Capture(path)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before.Equal(after) {
		t.Fatalf("expected inode/mtime change after rename: %+v == %+v", before, after)
	}
}

func TestCapture_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := Capture("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestCapture_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Capture(filepath.Join(dir, "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestCaptureSelf_Succeeds(t *testing.T) {
	t.Parallel()
	fp, err := CaptureSelf()
	if err != nil {
		t.Fatalf("CaptureSelf: %v", err)
	}
	if fp.Path == "" {
		t.Fatal("expected non-empty path")
	}
	if fp.Inode == 0 {
		t.Fatal("expected non-zero inode")
	}
}

// writeFile creates path with content. Mode 0o600 satisfies gosec G306;
// these tests don't actually exec the file, so the executable bit is
// irrelevant — the watcher only stats it.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

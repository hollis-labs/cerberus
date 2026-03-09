package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireAndRelease(t *testing.T) {
	tmp := t.TempDir()

	lock, err := AcquireLockAt(tmp, "test-svc")
	if err != nil {
		t.Fatalf("AcquireLockAt failed: %v", err)
	}

	// Lock file should exist
	lockPath := filepath.Join(tmp, ".cerberus", "locks", "test-svc.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file does not exist after acquire")
	}

	// Release should succeed and remove the file
	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock file still exists after release")
	}
}

func TestDoubleAcquireSameProcess(t *testing.T) {
	tmp := t.TempDir()

	lock1, err := AcquireLockAt(tmp, "test-svc")
	if err != nil {
		t.Fatalf("first AcquireLockAt failed: %v", err)
	}
	defer lock1.Release()

	// Second acquire from same process should succeed because Flock
	// allows the same process to re-lock.
	lock2, err := AcquireLockAt(tmp, "test-svc")
	if err != nil {
		t.Fatalf("second AcquireLockAt failed: %v", err)
	}
	defer lock2.Release()
}

func TestLockHolder(t *testing.T) {
	tmp := t.TempDir()

	lock, err := AcquireLockAt(tmp, "test-svc")
	if err != nil {
		t.Fatalf("AcquireLockAt failed: %v", err)
	}
	defer lock.Release()

	pid, since, err := LockHolderAt(tmp, "test-svc")
	if err != nil {
		t.Fatalf("LockHolderAt failed: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), pid)
	}
	if time.Since(since) > 5*time.Second {
		t.Errorf("acquired_at too old: %v", since)
	}
}

func TestStaleLockDetection(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".cerberus", "locks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Write a lock file with a dead PID (PID 999999 almost certainly doesn't exist)
	stalePID := 999999
	info := lockInfo{
		PID:        stalePID,
		AcquiredAt: time.Now().Add(-10 * time.Minute),
	}
	data, _ := json.Marshal(info)
	lockPath := filepath.Join(dir, "stale-svc.lock")
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	// Acquiring should break the stale lock and succeed
	lock, err := AcquireLockAt(tmp, "stale-svc")
	if err != nil {
		t.Fatalf("AcquireLockAt with stale lock failed: %v", err)
	}
	defer lock.Release()

	// Verify the new lock is held by us
	pid, _, err := LockHolderAt(tmp, "stale-svc")
	if err != nil {
		t.Fatalf("LockHolderAt failed: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d after stale break, got %d", os.Getpid(), pid)
	}
}

func TestReleaseCleanup(t *testing.T) {
	tmp := t.TempDir()

	lock, err := AcquireLockAt(tmp, "cleanup-svc")
	if err != nil {
		t.Fatalf("AcquireLockAt failed: %v", err)
	}

	lockPath := filepath.Join(tmp, ".cerberus", "locks", "cleanup-svc.lock")

	// File should exist while locked
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file missing while lock is held")
	}

	// Release
	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// File should be gone
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock file still exists after release")
	}

	// Double release should be safe (no-op)
	if err := lock.Release(); err != nil {
		t.Fatalf("double Release failed: %v", err)
	}
}

func TestLockHolderNoFile(t *testing.T) {
	tmp := t.TempDir()

	_, _, err := LockHolderAt(tmp, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent lock, got nil")
	}
}

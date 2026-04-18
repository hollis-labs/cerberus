package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubIdent treats any PID in daemonSet as a cerberus daemon.
type stubIdent struct {
	daemonSet map[int]bool
}

func (s stubIdent) IsCerberusDaemon(_ context.Context, pid int) (bool, error) {
	return s.daemonSet[pid], nil
}

func (s stubIdent) FindCerberusDaemonPIDs(_ context.Context) ([]int, error) {
	return nil, nil
}

func TestAcquireDaemonLock_HappyPath(t *testing.T) {
	base := t.TempDir()
	ident := stubIdent{daemonSet: map[int]bool{os.Getpid(): true}}

	lock, err := AcquireDaemonLockAt(base, ident)
	if err != nil {
		t.Fatalf("AcquireDaemonLockAt: %v", err)
	}
	defer lock.Release()

	path := filepath.Join(base, ".cerberus", "daemon.lock")
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatal("lock file missing after acquire")
	}

	pid, _, err := DaemonLockHolderAt(base)
	if err != nil {
		t.Fatalf("DaemonLockHolderAt: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquireDaemonLock_StaleHolder(t *testing.T) {
	// Pre-populate a lock file with a dead PID. Acquire should break it.
	base := t.TempDir()
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := daemonLockInfo{PID: 999999, AcquiredAt: time.Now().Add(-time.Hour)}
	data, _ := json.Marshal(stale)
	path := filepath.Join(dir, "daemon.lock")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	ident := stubIdent{daemonSet: map[int]bool{}} // 999999 is not a cerberus daemon

	lock, err := AcquireDaemonLockAt(base, ident)
	if err != nil {
		t.Fatalf("AcquireDaemonLockAt on stale: %v", err)
	}
	defer lock.Release()

	pid, _, err := DaemonLockHolderAt(base)
	if err != nil {
		t.Fatalf("DaemonLockHolderAt: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("after stale-break: holder pid = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquireDaemonLock_RefusesLiveCerberusHolder(t *testing.T) {
	// Simulate an already-held lock: open a fd, flock it, keep it open.
	// Then try to acquire from the same process — this would normally
	// succeed (flock is per-process) but our production uses NB and writes
	// PID under the lock. We can't directly emulate a separate live process
	// here, so we verify the "held and is cerberus daemon" branch by using
	// our own PID as the recorded holder and running AcquireDaemonLockAt
	// after taking a real flock via a second fd. On macOS/Linux flock is
	// advisory per-fd but LOCK_EX|LOCK_NB will fail a second acquire on a
	// different fd within the same process too when the file was opened
	// with different fd and the lock was acquired with flock(2) (BSD flock).
	//
	// To test deterministically we manually write the lock file with a
	// live PID (os.Getpid) that the ident says IS a cerberus daemon — and
	// hold the flock using an auxiliary fd in our own process.
	base := t.TempDir()
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "daemon.lock")

	// Hold an exclusive flock on the file from an aux fd.
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // test file, path is TempDir-local
	if err != nil {
		t.Fatalf("open aux: %v", err)
	}
	defer holder.Close()
	if flErr := flockEx(holder); flErr != nil {
		t.Fatalf("flock aux: %v", flErr)
	}
	defer flockUn(holder) //nolint:errcheck // test cleanup

	// Write holder info with our own PID.
	info := daemonLockInfo{PID: os.Getpid(), AcquiredAt: time.Now()}
	data, _ := json.Marshal(info)
	if _, wrErr := holder.WriteAt(data, 0); wrErr != nil {
		t.Fatalf("write holder info: %v", wrErr)
	}

	ident := stubIdent{daemonSet: map[int]bool{os.Getpid(): true}}

	_, err = AcquireDaemonLockAt(base, ident)
	if err == nil {
		t.Fatal("expected DaemonLockHeldError, got nil")
	}
	var held *DaemonLockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected DaemonLockHeldError, got %T: %v", err, err)
	}
	if held.HolderPID != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", held.HolderPID, os.Getpid())
	}
}

func TestAcquireDaemonLock_LiveButNotCerberus(t *testing.T) {
	// Lock held by a live process but the process is NOT a cerberus daemon
	// (e.g. PID reuse). We should treat as stale and take over.
	base := t.TempDir()
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "daemon.lock")

	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // test file, path is TempDir-local
	if err != nil {
		t.Fatalf("open aux: %v", err)
	}
	defer holder.Close()
	if flErr := flockEx(holder); flErr != nil {
		t.Fatalf("flock aux: %v", flErr)
	}
	// Note: we do NOT defer flockUn here — we want the Remove(path) from
	// AcquireDaemonLockAt to succeed, which it will on macOS/Linux even
	// while another fd holds flock (flock only locks on the file, not the
	// directory; Remove unlinks).

	info := daemonLockInfo{PID: os.Getpid(), AcquiredAt: time.Now()}
	data, _ := json.Marshal(info)
	if _, wrErr := holder.WriteAt(data, 0); wrErr != nil {
		t.Fatalf("write holder info: %v", wrErr)
	}

	// ident says our PID is NOT a cerberus daemon (PID reuse scenario).
	ident := stubIdent{daemonSet: map[int]bool{}}

	lock, err := AcquireDaemonLockAt(base, ident)
	if err != nil {
		t.Fatalf("AcquireDaemonLockAt should take over: %v", err)
	}
	defer lock.Release()
}

func TestReleaseRemovesFile(t *testing.T) {
	base := t.TempDir()
	ident := stubIdent{}
	lock, err := AcquireDaemonLockAt(base, ident)
	if err != nil {
		t.Fatalf("AcquireDaemonLockAt: %v", err)
	}
	path := filepath.Join(base, ".cerberus", "daemon.lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file persisted after Release: %v", err)
	}

	// Double-release must be safe.
	if err := lock.Release(); err != nil {
		t.Errorf("double Release: %v", err)
	}
}

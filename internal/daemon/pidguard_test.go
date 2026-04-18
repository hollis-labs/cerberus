package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRemoveDaemonPID(t *testing.T) {
	base := t.TempDir()
	pid := 4242

	if err := WriteDaemonPIDAt(base, pid); err != nil {
		t.Fatalf("WriteDaemonPIDAt: %v", err)
	}

	got, err := ReadDaemonPIDAt(base)
	if err != nil {
		t.Fatalf("ReadDaemonPIDAt: %v", err)
	}
	if got != pid {
		t.Errorf("got pid %d, want %d", got, pid)
	}

	// Temp file must not linger.
	tmp := filepath.Join(base, ".cerberus", "cerberus.pid.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf(".tmp file lingered after rename: %v", err)
	}

	RemoveDaemonPIDAt(base)
	if _, err := ReadDaemonPIDAt(base); err == nil {
		t.Error("expected error reading removed PID file")
	}
}

func TestWriteDaemonPIDAtomicRename(t *testing.T) {
	// The atomic-write path is: write tmp -> rename. Verify the rename
	// happens by observing that the final path exists and the tmp doesn't.
	base := t.TempDir()
	if err := WriteDaemonPIDAt(base, 777); err != nil {
		t.Fatalf("WriteDaemonPIDAt: %v", err)
	}
	final := filepath.Join(base, ".cerberus", "cerberus.pid")
	if _, err := os.Stat(final); err != nil {
		t.Errorf("final pid file missing: %v", err)
	}
	tmp := final + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf(".tmp file present: %v", err)
	}
}

func TestReadCorruptDaemonPID(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, ".cerberus")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cerberus.pid"), []byte("not-a-pid"), 0600); err != nil {
		t.Fatalf("write corrupt pid: %v", err)
	}
	if _, err := ReadDaemonPIDAt(base); err == nil {
		t.Error("expected error for corrupt pid file")
	}
}

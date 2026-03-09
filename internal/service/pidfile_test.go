package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/config"
)

func TestWriteReadRemovePIDFile(t *testing.T) {
	base := t.TempDir()
	serviceID := "test-service"
	pid := 12345

	// Write
	if err := WritePIDFileAt(base, serviceID, pid); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}

	// Read
	got, err := ReadPIDFileAt(base, serviceID)
	if err != nil {
		t.Fatalf("ReadPIDFileAt: %v", err)
	}
	if got != pid {
		t.Errorf("got pid %d, want %d", got, pid)
	}

	// Remove
	if err := RemovePIDFileAt(base, serviceID); err != nil {
		t.Fatalf("RemovePIDFileAt: %v", err)
	}

	// Read after removal should fail
	_, err = ReadPIDFileAt(base, serviceID)
	if err == nil {
		t.Error("expected error reading removed PID file")
	}
}

func TestValidateDetectsDeadPID(t *testing.T) {
	base := t.TempDir()
	serviceID := "dead-service"

	// Write a PID that almost certainly doesn't exist (very high number)
	deadPID := 4999999
	if err := WritePIDFileAt(base, serviceID, deadPID); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}

	pid, alive := ValidatePIDFileAt(base, serviceID)
	if alive {
		t.Errorf("expected dead PID %d to not be alive", deadPID)
	}
	if pid != deadPID {
		t.Errorf("got pid %d, want %d", pid, deadPID)
	}
}

func TestValidateDetectsLivePID(t *testing.T) {
	base := t.TempDir()
	serviceID := "live-service"

	// Use our own PID — guaranteed to be alive
	livePID := os.Getpid()
	if err := WritePIDFileAt(base, serviceID, livePID); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}

	pid, alive := ValidatePIDFileAt(base, serviceID)
	if !alive {
		t.Errorf("expected own PID %d to be alive", livePID)
	}
	if pid != livePID {
		t.Errorf("got pid %d, want %d", pid, livePID)
	}
}

func TestCleanStalePIDFiles(t *testing.T) {
	base := t.TempDir()

	// Write a stale PID file (dead process)
	if err := WritePIDFileAt(base, "stale-svc", 4999999); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}
	// Write a meta file for the stale service
	if err := WriteMetaFileAt(base, "stale-svc", PIDMeta{
		PID:       4999999,
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteMetaFileAt: %v", err)
	}

	// Write a live PID file (our own process)
	if err := WritePIDFileAt(base, "live-svc", os.Getpid()); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}

	if err := CleanStalePIDFilesAt(base); err != nil {
		t.Fatalf("CleanStalePIDFilesAt: %v", err)
	}

	// Stale should be gone (both .pid and .meta)
	dir, _ := pidDirAt(base)
	stalePID := filepath.Join(dir, "stale-svc.pid")
	staleMeta := filepath.Join(dir, "stale-svc.meta")
	if _, err := os.Stat(stalePID); !os.IsNotExist(err) {
		t.Error("stale PID file was not cleaned up")
	}
	if _, err := os.Stat(staleMeta); !os.IsNotExist(err) {
		t.Error("stale meta file was not cleaned up")
	}

	// Live should remain
	livePID := filepath.Join(dir, "live-svc.pid")
	if _, err := os.Stat(livePID); err != nil {
		t.Error("live PID file was incorrectly removed")
	}
}

func TestMetaFileRoundTrip(t *testing.T) {
	base := t.TempDir()
	serviceID := "meta-svc"
	now := time.Now().Truncate(time.Second) // JSON loses sub-second in some cases

	meta := PIDMeta{
		PID:             42,
		StartedAt:       now,
		ConfigHash:      "abc123",
		CerberusVersion: "0.1.0",
	}

	if err := WriteMetaFileAt(base, serviceID, meta); err != nil {
		t.Fatalf("WriteMetaFileAt: %v", err)
	}

	got, err := ReadMetaFileAt(base, serviceID)
	if err != nil {
		t.Fatalf("ReadMetaFileAt: %v", err)
	}

	if got.PID != meta.PID {
		t.Errorf("PID: got %d, want %d", got.PID, meta.PID)
	}
	if !got.StartedAt.Equal(meta.StartedAt) {
		t.Errorf("StartedAt: got %v, want %v", got.StartedAt, meta.StartedAt)
	}
	if got.ConfigHash != meta.ConfigHash {
		t.Errorf("ConfigHash: got %q, want %q", got.ConfigHash, meta.ConfigHash)
	}
	if got.CerberusVersion != meta.CerberusVersion {
		t.Errorf("CerberusVersion: got %q, want %q", got.CerberusVersion, meta.CerberusVersion)
	}
}

func TestConfigHash(t *testing.T) {
	def := config.ServiceDef{
		ID:      "test",
		Dir:     "/tmp/test",
		Port:    8080,
		Command: []string{"./server", "--port", "8080"},
		EnvFile: ".env",
	}

	hash1 := ConfigHash(def)
	if hash1 == "" {
		t.Error("ConfigHash returned empty string")
	}

	// Same config should produce same hash
	hash2 := ConfigHash(def)
	if hash1 != hash2 {
		t.Error("ConfigHash not deterministic")
	}

	// Different config should produce different hash
	def.Port = 9090
	hash3 := ConfigHash(def)
	if hash1 == hash3 {
		t.Error("ConfigHash should differ for different configs")
	}
}

func TestRemovePIDFileAlsoRemovesMeta(t *testing.T) {
	base := t.TempDir()
	serviceID := "cleanup-svc"

	// Write both files
	if err := WritePIDFileAt(base, serviceID, 123); err != nil {
		t.Fatalf("WritePIDFileAt: %v", err)
	}
	if err := WriteMetaFileAt(base, serviceID, PIDMeta{PID: 123}); err != nil {
		t.Fatalf("WriteMetaFileAt: %v", err)
	}

	// Remove should clean up both
	if err := RemovePIDFileAt(base, serviceID); err != nil {
		t.Fatalf("RemovePIDFileAt: %v", err)
	}

	dir, _ := pidDirAt(base)
	if _, err := os.Stat(filepath.Join(dir, serviceID+".pid")); !os.IsNotExist(err) {
		t.Error("PID file not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, serviceID+".meta")); !os.IsNotExist(err) {
		t.Error("meta file not removed")
	}
}

func TestReadPIDFileNotExist(t *testing.T) {
	base := t.TempDir()
	_, err := ReadPIDFileAt(base, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent PID file")
	}
}

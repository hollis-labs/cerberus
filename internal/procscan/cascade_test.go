package procscan

import (
	"testing"
)

func TestCascadeKillStaleSubprocesses_ZeroFingerprintNoOp(t *testing.T) {
	t.Parallel()
	out := CascadeKillStaleSubprocesses(BinaryFingerprint{}, discardLogger())
	if len(out) != 0 {
		t.Errorf("expected no outcomes for zero fingerprint, got %v", out)
	}
}

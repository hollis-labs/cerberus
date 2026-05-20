package daemon

import "testing"

func TestLaunchdSpawnedSelf_TrueWhenXPCMatches(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", CanonicalDaemonServiceLabel)
	if !LaunchdSpawnedSelf() {
		t.Fatal("LaunchdSpawnedSelf = false; expected true when XPC_SERVICE_NAME matches canonical label")
	}
	if DaemonOrigin() != "launchd" {
		t.Errorf("DaemonOrigin = %q, want %q", DaemonOrigin(), "launchd")
	}
}

func TestLaunchdSpawnedSelf_FalseWhenXPCEmpty(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "")
	if LaunchdSpawnedSelf() {
		t.Fatal("LaunchdSpawnedSelf = true; expected false when XPC_SERVICE_NAME is empty")
	}
	if DaemonOrigin() != "manual" {
		t.Errorf("DaemonOrigin = %q, want %q", DaemonOrigin(), "manual")
	}
}

func TestLaunchdSpawnedSelf_FalseForUnrelatedXPC(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "com.apple.something.else")
	if LaunchdSpawnedSelf() {
		t.Fatal("LaunchdSpawnedSelf = true for unrelated XPC label; expected false")
	}
	if DaemonOrigin() != "manual" {
		t.Errorf("DaemonOrigin = %q, want %q", DaemonOrigin(), "manual")
	}
}

func TestLaunchdServiceTarget_Format(t *testing.T) {
	target := LaunchdServiceTarget()
	if target == "" {
		t.Fatal("LaunchdServiceTarget returned empty string")
	}
	// gui/<uid>/<label>; only assert the label suffix since uid varies.
	suffix := "/" + CanonicalDaemonServiceLabel
	if got := target[len(target)-len(suffix):]; got != suffix {
		t.Errorf("LaunchdServiceTarget = %q; expected suffix %q", target, suffix)
	}
}

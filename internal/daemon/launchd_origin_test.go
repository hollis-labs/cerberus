package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// The daemon's job is addressed under the label its plist is installed as:
// the legacy one only while it alone is installed, so a daemon from before
// the rename is still kickstarted where it runs.
func TestLaunchdServiceTarget_Format(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		t.Fatal(err)
	}
	suffix := func() string {
		target := LaunchdServiceTarget()
		return target[strings.LastIndex(target, "/")+1:]
	}
	if got := suffix(); got != CanonicalDaemonServiceLabel {
		t.Errorf("nothing installed: %q", got)
	}
	legacy := filepath.Join(agents, LegacyDaemonServiceLabel+".plist")
	if err := os.WriteFile(legacy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := suffix(); got != LegacyDaemonServiceLabel || !LaunchdManagedDaemonExists() {
		t.Errorf("only the legacy plist: %q", got)
	}
	if err := os.WriteFile(filepath.Join(agents, CanonicalDaemonServiceLabel+".plist"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := suffix(); got != CanonicalDaemonServiceLabel {
		t.Errorf("both plists: %q", got)
	}
}

// A daemon launchd started under either label is the launchd-managed one.
func TestLaunchdSpawnedSelfAcceptsTheLegacyLabel(t *testing.T) {
	for label, want := range map[string]bool{CanonicalDaemonServiceLabel: true, LegacyDaemonServiceLabel: true, "com.example.other": false} {
		t.Setenv("XPC_SERVICE_NAME", label)
		if got := LaunchdSpawnedSelf(); got != want {
			t.Errorf("%s: %v", label, got)
		}
	}
}

func TestUnderUserManager(t *testing.T) {
	cases := map[string]bool{
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/cerberus.service\n":                         true,
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/com.hollis-labs.cerberus.service\n":         true,
		"0::/user.slice/user-1000.slice/session-3.scope\n":                                                      false,
		"0::/user.slice/user-1001.slice/user@1001.service/app.slice/cerberus.service\n":                         false,
		"12:pids:/user.slice/user-1000.slice/user@1000.service/app.slice/x.service\n0::/system.slice/y.service": true,
	}
	for cgroup, want := range cases {
		if got := underUserManager(cgroup, 1000); got != want {
			t.Errorf("underUserManager(%q) = %v, want %v", cgroup, got, want)
		}
	}
}

func TestSystemdSpawnedSelfNeedsInvocationID(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("XPC_SERVICE_NAME", "")
	if SystemdSpawnedSelf() {
		t.Fatal("SystemdSpawnedSelf without INVOCATION_ID")
	}
	if DaemonOrigin() != "manual" {
		t.Fatalf("DaemonOrigin = %q, want manual", DaemonOrigin())
	}
}

// The daemon's unit is addressed under the name it is installed as: a
// hand-written cerberus.service only while it alone is installed.
func TestSystemdDaemonUnitPrefersTheCanonicalUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SystemdDaemonUnit(); got != CanonicalDaemonUnit {
		t.Errorf("nothing installed: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, HandWrittenDaemonUnit), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SystemdDaemonUnit(); got != HandWrittenDaemonUnit {
		t.Errorf("only the hand-written unit: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, CanonicalDaemonUnit), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SystemdDaemonUnit(); got != CanonicalDaemonUnit {
		t.Errorf("both installed: %q", got)
	}
}

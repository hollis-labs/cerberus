package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireMake skips the test if `make` isn't installed on the runner; CI hosts
// without make should not fail the suite for an integration smoke that's
// fundamentally about make's behavior.
func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available on PATH")
	}
}

func writeMakefile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(body), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
}

func TestRunInstall_SkipsWhenInstallTargetAbsent(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeMakefile(t, dir, "build:\n\t@echo built\n")

	skipped, out, err := RunInstall(ProcessSpec{Dir: dir})
	if err != nil {
		t.Fatalf("RunInstall returned error for missing install target: %v (out=%q)", err, out)
	}
	if !skipped {
		t.Fatalf("RunInstall should have skipped (no install target); skipped=%v, out=%q", skipped, out)
	}
	if out != "" {
		t.Fatalf("expected empty output on skip, got %q", out)
	}
}

func TestRunInstall_RunsWhenInstallTargetPresent(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "installed.marker")
	writeMakefile(t, dir, "install:\n\t@touch "+marker+"\n")

	skipped, _, err := RunInstall(ProcessSpec{Dir: dir})
	if err != nil {
		t.Fatalf("RunInstall returned error: %v", err)
	}
	if skipped {
		t.Fatalf("RunInstall should not have skipped when install target is present")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("install target side-effect missing: %v", statErr)
	}
}

func TestRunInstall_FailsWhenInstallErrors(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeMakefile(t, dir, "install:\n\t@false\n")

	skipped, _, err := RunInstall(ProcessSpec{Dir: dir})
	if err == nil {
		t.Fatalf("RunInstall should have errored for a failing install target")
	}
	if skipped {
		t.Fatalf("RunInstall should not report skipped when target ran and failed")
	}
}

func TestRunInstall_RequiresWorkingDir(t *testing.T) {
	_, _, err := RunInstall(ProcessSpec{})
	if err == nil {
		t.Fatalf("RunInstall should error when spec.Dir is empty")
	}
}

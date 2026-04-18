//go:build darwin

package procscan

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// buildHelperBinary compiles a tiny long-lived helper binary at the
// given path. We can't just copy /bin/sleep on macOS because Apple's
// kernel kills code-signed binaries that have been moved out of their
// signed location ("invalid code signature" → instant zombie). A
// freshly-compiled Go binary has no code signature requirement and
// runs cleanly from anywhere.
func buildHelperBinary(t *testing.T, dest string) {
	t.Helper()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "main.go")
	const program = `package main

import (
	"os"
	"time"
)

func main() {
	// Long-lived helper for procscan integration tests. Exits on
	// SIGTERM/SIGKILL — the Go runtime handles both by default.
	if len(os.Args) > 1 {
		_ = os.Args[1] // ignore arg
	}
	time.Sleep(60 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", dest, src) //nolint:gosec // test-controlled inputs
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
}

// TestDarwinEnumerator_RealSubprocess verifies the platform code-path
// against a real long-lived subprocess. We compile a tiny helper binary
// into a temp path, run it, capture the fingerprint, scan, and assert
// the helper PID is returned by the real darwin enumerator. Then we
// cascade-kill it via KillAndWait and assert it exited.
func TestDarwinEnumerator_RealSubprocess(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	if testing.Short() {
		t.Skip("integration test (compiles a helper binary) — skipped under -short")
	}

	helper := filepath.Join(t.TempDir(), "scan-helper")
	buildHelperBinary(t, helper)

	// Capture BEFORE launching — matches the rebuild-path order of
	// operations (capture → build → scan → kill).
	fp, err := Capture(helper)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	cmd := exec.Command(helper, "60") //nolint:gosec // test-controlled inputs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}
	helperPID := cmd.Process.Pid
	// Reap the child as soon as it exits so KillAndWait's Alive()
	// (which uses signal-0 and returns true for zombies) doesn't time
	// out. In production cascade-kill targets are foreign subprocesses
	// owned by other parents — those parents do their own reaping.
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-helperPID, syscall.SIGKILL)
		<-waitDone
	})

	// Give the kernel a moment to register proc_listpids visibility.
	time.Sleep(100 * time.Millisecond)

	matched, err := PIDsByFingerprint(fp, discardLogger())
	if err != nil {
		t.Fatalf("PIDsByFingerprint: %v", err)
	}

	found := false
	for _, pid := range matched {
		if pid == helperPID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("helper PID %d not in matched set %v (fp=%+v)", helperPID, matched, fp)
	}

	// Cascade-kill it via the public KillAndWait.
	out := KillAndWait([]int{helperPID}, 2*time.Second, discardLogger())
	if len(out) != 1 || !out[0].Exited {
		t.Fatalf("KillAndWait did not exit helper: %+v", out)
	}
}

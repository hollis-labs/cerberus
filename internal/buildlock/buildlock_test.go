package buildlock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExclusionAliasesAndRelease(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir, Holder{ResourceID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	alias := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(alias, Holder{ResourceID: "second"})
	var busy *ErrBuildInProgress
	if !errors.As(err, &busy) || busy.Holder.ResourceID != "first" || busy.Holder.PID != os.Getpid() {
		t.Fatalf("bad contention result: %v", err)
	}
	independent, err := Acquire(t.TempDir(), Holder{})
	if err != nil {
		t.Fatal(err)
	}
	independent()
	release()
	release()
	again, err := Acquire(dir, Holder{ResourceID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestKernelReleasesLockOnProcessExit(t *testing.T) {
	if dir := os.Getenv("CERBERUS_BUILD_LOCK_HELPER"); dir != "" {
		if _, err := Acquire(dir, Holder{ResourceID: "child"}); err != nil {
			os.Exit(2)
		}
		fmt.Println("locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(0) // deliberately exit without invoking release
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKernelReleasesLockOnProcessExit$") //nolint:gosec // runs this test binary as a lock holder
	cmd.Env = append(os.Environ(), "CERBERUS_BUILD_LOCK_HELPER="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire: %q %v", line, err)
	}
	_, err = Acquire(dir, Holder{})
	var busy *ErrBuildInProgress
	if !errors.As(err, &busy) || busy.Holder.ResourceID != "child" {
		t.Fatalf("cross-process exclusion failed: %v", err)
	}
	if err = in.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(dir, Holder{})
	if err != nil {
		t.Fatalf("process exit leaked lock: %v", err)
	}
	release()
}

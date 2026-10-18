package outpostexecutor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

func TestLaunchdLogsCancellationAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	agents := filepath.Join(dir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	logfile := filepath.Join(dir, "log")
	if err := os.WriteFile(logfile, []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(agents, "example.plist")
	body := []byte(`<plist><dict><key>StandardOutPath</key><string>` + logfile + `</string></dict></plist>`)
	if err := os.WriteFile(plist, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if _, err := launchdLogs(ctx, outpost.Request{Verb: "logs", Unit: "example"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("canceled request did not return promptly")
	}
	for _, path := range []string{plist, logfile} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := launchdLogs(ctx, outpost.Request{Verb: "logs", Unit: "example"})
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
			t.Fatalf("special file did not refuse promptly: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plist, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegularFileCancellationWhileWaitingForIO(t *testing.T) {
	// Model all filesystem slots stuck in the kernel. Cancellation must release
	// the caller without starting an unbounded number of further workers.
	for i := 0; i < cap(fileIO); i++ {
		fileIO <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(fileIO); i++ {
			<-fileIO
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := readRegular(ctx, "unused", 1024, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("I/O queue ignored cancellation")
	}
}

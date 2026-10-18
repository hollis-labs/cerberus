package outpostexecutor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// Security regression tests: every operation uses a fake runner or temp files.
func TestReviewControlPlaneBypasses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux command selection")
	}
	for _, tc := range []struct {
		name, verb, unit string
		protected        []string
	}{
		{"manager-exit", "start", "exit.target", nil},
		{"manager-shutdown", "start", "shutdown.target", nil},
		{"generated-outpost-name", "stop", "com.hollis-labs.cerberus.outpost.outpost.service", nil},
		{"configured-short-name", "stop", "custom-outpost.service", []string{"custom-outpost"}},
		{"configured-space", "restart", "custom-outpost.service", []string{" custom-outpost.service"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			p := Plugin{Protected: tc.protected, Run: func(_ context.Context, cmd string, args []string) (outpost.Result, error) {
				reached = true
				t.Logf("native call: %s %q", cmd, args)
				return outpost.Result{}, nil
			}}
			_, err := p.Execute(context.Background(), outpost.Request{Verb: tc.verb, Unit: tc.unit})
			if err == nil || reached {
				t.Errorf("control-plane mutation accepted")
			}
		})
	}
}

func TestReviewArgumentValidation(t *testing.T) {
	for _, unit := range []string{"--all", " bad", "bad name", "x\nservice", "../secret", "x/../secret", "x;echo", "@x", strings.Repeat("a", 256), "x\x00"} {
		if err := (outpost.Request{Verb: "restart", Unit: unit}).Validate(); err == nil {
			t.Errorf("accepted %q", unit)
		}
	}
	for _, unit := range []string{"example@instance.service", "x..service", "Example.service"} {
		cmd, args, err := Command("linux", 1000, outpost.Request{Verb: "restart", Unit: unit})
		if err != nil || cmd != "systemctl" || args[len(args)-1] != unit || args[len(args)-2] != "--" {
			t.Fatalf("unexpected command %s %q %v", cmd, args, err)
		}
	}
	for _, verb := range []string{"disable", "exec", "enable", "daemon-reload"} {
		if err := (outpost.Request{Verb: verb, Unit: "example.service"}).Validate(); err == nil {
			t.Errorf("accepted %s", verb)
		}
	}
	for _, verb := range []string{"start", "stop", "restart"} {
		cmd, args, err := Command("darwin", 501, outpost.Request{Verb: verb, Unit: "example"})
		if err != nil || cmd != "launchctl" || !strings.Contains(strings.Join(args, " "), "gui/501") {
			t.Fatalf("unexpected launchd domain: %s %q %v", cmd, args, err)
		}
	}
}

func TestReviewLaunchdFIFOBlocks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	agents := filepath.Join(dir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "log.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	plist := `<plist><dict><key>StandardOutPath</key><string>` + fifo + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(agents, "example.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := launchdLogs(context.Background(), outpost.Request{Verb: "logs", Unit: "example", Lines: 1})
		done <- err
	}()
	select {
	case err := <-done:
		t.Logf("returned: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Release only our fixture FIFO, so this reproduction never leaves a hung goroutine.
		f, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0600) // #nosec G304 -- releases only the temporary fixture FIFO.
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		select {
		case err := <-done:
			t.Logf("released FIFO: %v", err)
		case <-time.After(time.Second):
			t.Fatal("fixture failed to release")
		}
		t.Error("log read blocks before regular-file check; launchdLogs exceeded its filesystem bound")
	}
}

func TestReviewChildTimeoutAndTailBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	result, err := run(ctx, "/bin/sleep", []string{"5"})
	if err != nil || result.ExitCode == 0 || time.Since(began) > time.Second {
		t.Fatalf("timeout: %+v %v %s", result, err, time.Since(began))
	}
	path := filepath.Join(t.TempDir(), "log")
	if err = os.WriteFile(path, []byte(strings.Repeat("line\n", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	b, truncated, err := tail(context.Background(), path, 3)
	if err != nil || !truncated || string(b) != "line\nline\nline" {
		t.Fatalf("tail: %q %t %v", b, truncated, err)
	}
}

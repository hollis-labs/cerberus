package service

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// childEnv marks the re-executed test binary that does the logging, so a
// hang is a child the parent kills rather than a wedged test process.
const childEnv = "CERBERUS_TEST_REDACTED_DEFAULT_LOGGER_CHILD"

// After the daemon makes the default logger redact, logging through
// slog.Default() and through the log package returns, and redacts. It used
// to wrap slog's default handler, which writes through the log package that
// SetDefault had pointed back at it, and the first record hung for good.
func TestTheRedactingDefaultLoggerDoesNotDeadlock(t *testing.T) {
	if os.Getenv(childEnv) == "1" {
		redactDefaultLoggerTo(os.Stdout)
		slog.Default().Info("pipeline.stage.start", "stage", "one")
		slog.Info("through the package function", "authorization", "Bearer abcdefghijklmnopqrstuvwxyz0123456789")
		log.Printf("through the log package: token=supersecretvalue123456")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTheRedactingDefaultLoggerDoesNotDeadlock$", "-test.count=1") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), childEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("logging through the default logger hung:\n%s", out.String())
	}
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"pipeline.stage.start", "through the package function", "through the log package"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, secret := range []string{"abcdefghijklmnopqrstuvwxyz0123456789", "supersecretvalue123456"} {
		if strings.Contains(got, secret) {
			t.Errorf("not redacted: %q in:\n%s", secret, got)
		}
	}
}

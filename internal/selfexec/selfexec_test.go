package selfexec

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// quietLogger discards all log output so tests don't pollute stderr.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// exitRecorder returns an ExitFn that records the code and signals via
// channel when called. Goroutine-safe.
func exitRecorder() (exitFn func(int), called <-chan int, code *atomic.Int32) {
	ch := make(chan int, 1)
	c := &atomic.Int32{}
	c.Store(-1)
	return func(n int) {
		// nolint:gosec // exit codes are bounded small ints; #nosec G115
		c.Store(int32(n))
		select {
		case ch <- n:
		default:
		}
	}, ch, c
}

// stagedBinary writes a file at dir/binary and returns its path. Mode
// 0o600 satisfies gosec G306; these tests only stat the file (no exec),
// so executable bits are irrelevant to the behavior under test.
func stagedBinary(t *testing.T, dir string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "binary")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("stage binary: %v", err)
	}
	return path
}

func TestDefaultOptions_TestContextDisablesWatcher(t *testing.T) {
	// We're running under `go test`, so Args[0] should match.
	opts := DefaultOptions()
	if opts.Interval < time.Hour {
		t.Fatalf("expected test-context interval >= 1h, got %v", opts.Interval)
	}
	if opts.ExitFn == nil {
		t.Fatal("expected non-nil ExitFn")
	}
	if opts.Logger == nil {
		t.Fatal("expected non-nil Logger")
	}
}

func TestWatchAndExit_QuiescentDoesNotExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, _ := exitRecorder()
	opts := Options{
		Interval: 25 * time.Millisecond,
		Logger:   quietLogger(),
		ExitFn:   exitFn,
		ExePath:  path,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		WatchAndExit(ctx, opts)
		close(done)
	}()

	// Three intervals of quiescence should not trigger an exit.
	select {
	case <-called:
		t.Fatal("ExitFn was called for an unchanged binary")
	case <-time.After(100 * time.Millisecond):
		// expected: no exit
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchAndExit did not return after ctx cancel")
	}
}

func TestWatchAndExit_DetectsInodeChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, code := exitRecorder()
	opts := Options{
		Interval: 20 * time.Millisecond,
		Logger:   quietLogger(),
		ExitFn:   exitFn,
		ExePath:  path,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go WatchAndExit(ctx, opts)

	// Give the watcher one tick to capture its baseline.
	time.Sleep(30 * time.Millisecond)

	// Atomic replace via rename → new inode.
	staging := filepath.Join(dir, "staging")
	if err := os.WriteFile(staging, []byte("v2-replaced"), 0o600); err != nil {
		t.Fatalf("stage replacement: %v", err)
	}
	if err := os.Rename(staging, path); err != nil {
		t.Fatalf("rename: %v", err)
	}

	select {
	case <-called:
		if got := code.Load(); got != 0 {
			t.Fatalf("expected exit code 0, got %d", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ExitFn was not called within deadline after inode change")
	}
}

func TestWatchAndExit_DetectsMtimeChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, code := exitRecorder()
	opts := Options{
		Interval: 20 * time.Millisecond,
		Logger:   quietLogger(),
		ExitFn:   exitFn,
		ExePath:  path,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go WatchAndExit(ctx, opts)
	time.Sleep(30 * time.Millisecond)

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	select {
	case <-called:
		if got := code.Load(); got != 0 {
			t.Fatalf("expected exit code 0, got %d", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ExitFn was not called within deadline after mtime change")
	}
}

func TestWatchAndExit_FingerprintFailureDoesNotExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, _ := exitRecorder()
	opts := Options{
		Interval: 20 * time.Millisecond,
		Logger:   quietLogger(),
		ExitFn:   exitFn,
		ExePath:  path,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go WatchAndExit(ctx, opts)
	time.Sleep(30 * time.Millisecond)

	// Delete the file. Subsequent fingerprints fail — but the watcher
	// must NOT exit; deleted-binary is weird, safest to keep serving.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	select {
	case <-called:
		t.Fatal("ExitFn was called on fingerprint failure; expected log + continue")
	case <-time.After(150 * time.Millisecond):
		// expected
	}
}

func TestWatchAndExit_StartupResolutionFailureLogsAndReturns(t *testing.T) {
	t.Parallel()

	exitFn, called, _ := exitRecorder()
	opts := Options{
		Interval: 10 * time.Millisecond,
		Logger:   quietLogger(),
		ExitFn:   exitFn,
		ExePath:  "/definitely/does/not/exist/cerberus-binary",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		WatchAndExit(ctx, opts)
		close(done)
	}()

	select {
	case <-done:
		// expected: returns immediately, no exit call
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WatchAndExit did not return after baseline-resolution failure")
	}
	select {
	case <-called:
		t.Fatal("ExitFn was called despite baseline-resolution failure")
	default:
	}
}

func TestCheckAndExitIfStale_FirstCallStoresBaseline(t *testing.T) {
	resetBaselines()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, _ := exitRecorder()
	opts := Options{
		Logger:  quietLogger(),
		ExitFn:  exitFn,
		ExePath: path,
	}

	if CheckAndExitIfStale(opts) {
		t.Fatal("first call should establish baseline and return false")
	}
	select {
	case <-called:
		t.Fatal("ExitFn was called on first check")
	default:
	}
}

func TestCheckAndExitIfStale_DetectsChange(t *testing.T) {
	resetBaselines()
	dir := t.TempDir()
	path := stagedBinary(t, dir, []byte("v1"))

	exitFn, called, code := exitRecorder()
	opts := Options{
		Logger:  quietLogger(),
		ExitFn:  exitFn,
		ExePath: path,
	}

	// Establish baseline.
	if CheckAndExitIfStale(opts) {
		t.Fatal("baseline call should return false")
	}

	// Replace the file.
	staging := filepath.Join(dir, "staging")
	if err := os.WriteFile(staging, []byte("v2"), 0o600); err != nil {
		t.Fatalf("stage replacement: %v", err)
	}
	if err := os.Rename(staging, path); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if !CheckAndExitIfStale(opts) {
		t.Fatal("expected stale check to return true")
	}
	select {
	case <-called:
		if got := code.Load(); got != 0 {
			t.Fatalf("expected exit code 0, got %d", got)
		}
	default:
		t.Fatal("ExitFn was not invoked on detected change")
	}
}

func TestCheckAndExitIfStale_FingerprintFailureReturnsFalse(t *testing.T) {
	resetBaselines()
	exitFn, called, _ := exitRecorder()
	opts := Options{
		Logger:  quietLogger(),
		ExitFn:  exitFn,
		ExePath: "/definitely/does/not/exist/cerberus-binary",
	}
	if CheckAndExitIfStale(opts) {
		t.Fatal("expected false on fingerprint failure")
	}
	select {
	case <-called:
		t.Fatal("ExitFn must not be called on fingerprint failure")
	default:
	}
}

func TestOptionsWithDefaults_FillsZeroValues(t *testing.T) {
	t.Parallel()
	opts := Options{}.withDefaults()
	if opts.Interval != DefaultInterval {
		t.Fatalf("expected DefaultInterval, got %v", opts.Interval)
	}
	if opts.Logger == nil {
		t.Fatal("expected non-nil logger")
	}
	if opts.ExitFn == nil {
		t.Fatal("expected non-nil ExitFn")
	}
}

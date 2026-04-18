package selfexec

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
)

// DefaultInterval is the polling cadence used by DefaultOptions when no
// override is supplied. 30s is fast enough that operators don't notice
// the gap and slow enough that fingerprinting overhead is negligible.
const DefaultInterval = 30 * time.Second

// testInterval is the value DefaultOptions returns when it detects a
// `go test` context. Effectively disables the watcher under tests; a test
// that wants to exercise it constructs Options directly with a short
// interval and an injected ExitFn.
const testInterval = time.Hour

// Options tunes the self-exec watcher. Zero values are filled in with
// production defaults. Tests can inject ExitFn / ExePath to assert
// behavior without calling os.Exit.
type Options struct {
	// Interval between fingerprint checks. Defaults to DefaultInterval.
	Interval time.Duration

	// Logger used for structured events. Defaults to slog.Default().
	Logger *slog.Logger

	// ExitFn is invoked with code 0 on detected change. Defaults to
	// os.Exit. Tests inject a no-op or counter to assert calls.
	ExitFn func(code int)

	// ExePath overrides automatic resolution of the running binary's
	// path. Production leaves this empty (resolved via os.Executable);
	// tests point it at a temp file they can mutate.
	ExePath string
}

// DefaultOptions returns Options ready for production use.
//
// If the process appears to be running under `go test` (os.Args[0] ends
// in ".test" or contains ".test/" or the GO_TESTING env var is set), the
// interval is set to one hour to effectively disable the watcher. This
// prevents incidental subprocess self-exits during test runs that happen
// to import this package.
func DefaultOptions() Options {
	interval := DefaultInterval
	if isTestContext() {
		interval = testInterval
	}
	return Options{
		Interval: interval,
		Logger:   slog.Default(),
		ExitFn:   os.Exit,
	}
}

func isTestContext() bool {
	if os.Getenv("GO_TESTING") == "1" {
		return true
	}
	if len(os.Args) == 0 {
		return false
	}
	arg0 := os.Args[0]
	return strings.HasSuffix(arg0, ".test") || strings.Contains(arg0, ".test/")
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ExitFn == nil {
		o.ExitFn = os.Exit
	}
	return o
}

// resolvePath returns Options.ExePath if set, else os.Executable().
func (o Options) resolvePath() (string, error) {
	if o.ExePath != "" {
		return o.ExePath, nil
	}
	return os.Executable()
}

// captureWith fingerprints whichever path Options resolves to.
func (o Options) captureWith() (Fingerprint, error) {
	path, err := o.resolvePath()
	if err != nil {
		return Fingerprint{}, err
	}
	return Capture(path)
}

// WatchAndExit blocks until ctx is canceled or a binary change is
// detected. On detected change, it logs selfexec.exiting and calls
// opts.ExitFn(0). On fingerprint failure mid-run, it logs and continues
// (a transiently missing binary is weird but safer to keep serving
// tools than to fall over).
//
// Typical use: `go selfexec.WatchAndExit(ctx, selfexec.DefaultOptions())`.
func WatchAndExit(ctx context.Context, opts Options) {
	opts = opts.withDefaults()
	logger := opts.Logger

	start, err := opts.captureWith()
	if err != nil {
		logger.Warn("selfexec.unsupported_platform",
			"error", err.Error(),
			"message", "binary fingerprint unavailable; watcher disabled")
		return
	}

	logger.Info("selfexec.started",
		"binary_path", start.Path,
		"interval_ms", opts.Interval.Milliseconds())

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := opts.captureWith()
			if err != nil {
				logger.Warn("selfexec.check.failed",
					"error", err.Error())
				continue
			}
			if !current.Equal(start) {
				logger.Info("selfexec.check.changed",
					"binary_path", current.Path,
					"old_inode", start.Inode,
					"new_inode", current.Inode,
					"old_mtime", start.MTime.Format(time.RFC3339Nano),
					"new_mtime", current.MTime.Format(time.RFC3339Nano))
				logger.Info("selfexec.exiting",
					"reason", "binary on disk has been replaced")
				opts.ExitFn(0)
				// In production ExitFn=os.Exit never returns. In tests
				// the injected ExitFn returns; we stop the loop so the
				// goroutine exits cleanly.
				return
			}
		}
	}
}

// CheckAndExitIfStale is a one-shot variant of WatchAndExit. Suitable
// for hooking into a tool-call dispatch path so a stale subprocess can
// detect and exit on the very next request rather than waiting for the
// next ticker tick.
//
// Returns true if a change was detected and ExitFn was called; false
// otherwise. On fingerprint failure, returns false (don't kill the
// subprocess just because stat failed once).
//
// Note: because CheckAndExitIfStale has no startup baseline, it
// captures one on first call by storing it on opts.ExePath via the
// process-global registry below. For a stable baseline, callers should
// prefer WatchAndExit, which holds its own baseline in scope. This
// helper exists for code paths that can't run a goroutine.
func CheckAndExitIfStale(opts Options) bool {
	opts = opts.withDefaults()
	logger := opts.Logger

	current, err := opts.captureWith()
	if err != nil {
		logger.Warn("selfexec.check.failed", "error", err.Error())
		return false
	}

	baseline, ok := loadBaseline(current.Path)
	if !ok {
		storeBaseline(current)
		return false
	}

	if current.Equal(baseline) {
		return false
	}

	logger.Info("selfexec.check.changed",
		"binary_path", current.Path,
		"old_inode", baseline.Inode,
		"new_inode", current.Inode,
		"old_mtime", baseline.MTime.Format(time.RFC3339Nano),
		"new_mtime", current.MTime.Format(time.RFC3339Nano))
	logger.Info("selfexec.exiting",
		"reason", "binary on disk has been replaced")
	opts.ExitFn(0)
	return true
}

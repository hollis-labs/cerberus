package service

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/hollis-labs/cerberus/internal/redact"
)

var (
	lifecycleLogger *slog.Logger
	logOnce         sync.Once
)

// InitLifecycleLog sets up the structured lifecycle logger writing JSON to
// ~/.cerberus/cerberus.log. Safe to call multiple times; only the first call
// takes effect.
//
// The log is the operator's own: mode 0600, and a file an earlier version
// created 0644 is narrowed on open. Every record goes through
// redact.Handler, so a record logged on a request's context loses that
// request's credentials and every record gets the regex net. The daemon
// also wraps the process's default logger (RedactDefaultLogger).
func InitLifecycleLog() {
	logOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir := filepath.Join(home, ".cerberus")
		_ = os.MkdirAll(dir, 0755)

		path := filepath.Join(dir, "cerberus.log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the fixed log path under the user's home
		if err != nil {
			return
		}
		_ = f.Chmod(0o600)
		lifecycleLogger = slog.New(redact.NewHandler(slog.NewJSONHandler(f, nil)))
	})
}

// RedactDefaultLogger makes slog's default logger redact. Most of cerbapi,
// and the pipeline executor, log through slog.Default(), which in the daemon
// writes to launchd's stderr.log. Only the daemon calls this: it also routes
// the log package through slog, which a short-lived CLI command has no
// reason to have changed under it.
//
// The redacting handler writes to stderr through a handler of its own. It
// must not wrap slog's default handler: that one writes through the log
// package, which SetDefault then points back at the new default, so the
// first record re-entered the log package's lock and hung the caller for
// good (every pipeline run did).
func RedactDefaultLogger() { redactDefaultLoggerTo(os.Stderr) }

func redactDefaultLoggerTo(w io.Writer) {
	slog.SetDefault(slog.New(redact.NewHandler(slog.NewTextHandler(w, nil))))
}

// llog returns the lifecycle logger, falling back to slog.Default().
func llog() *slog.Logger {
	if lifecycleLogger != nil {
		return lifecycleLogger
	}
	return slog.Default()
}

// GetLogger returns the lifecycle logger for use by other packages.
func GetLogger() *slog.Logger {
	return llog()
}

package service

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

var (
	lifecycleLogger *slog.Logger
	logOnce         sync.Once
)

// InitLifecycleLog sets up the structured lifecycle logger writing JSON to
// ~/.cerberus/cerberus.log. Safe to call multiple times; only the first call
// takes effect.
func InitLifecycleLog() {
	logOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir := filepath.Join(home, ".cerberus")
		_ = os.MkdirAll(dir, 0755)

		f, err := os.OpenFile(filepath.Join(dir, "cerberus.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		lifecycleLogger = slog.New(slog.NewJSONHandler(f, nil))
	})
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

package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounceInterval is how long the watcher coalesces fsnotify events
// before firing a reload. Editors (vim, VS Code, etc.) frequently produce
// CREATE/RENAME/WRITE/CHMOD bursts per save; we want one reload per burst.
const DefaultDebounceInterval = 250 * time.Millisecond

// Watcher observes a config file via fsnotify and invokes a reload callback
// whenever the file changes (coalesced into bursts via debounce).
//
// The watcher is intentionally best-effort:
//   - fsnotify failures are logged but do not crash the daemon.
//   - Editors that rename-into-place (vim's atomic writes) trigger a REMOVE,
//     so we also watch the parent directory and re-add the watch on any
//     CREATE that matches our target filename.
//
// The caller owns the reload callback and is expected to route it through
// the same code path as SIGHUP / `cerberus daemon reload`.
type Watcher struct {
	path     string
	dir      string
	base     string
	debounce time.Duration
	onReload func()
	logger   *slog.Logger
	w        *fsnotify.Watcher
}

// NewWatcher constructs a file watcher. It does not start watching until
// Run() is called.
//
// onReload fires at most once per debounce window. If the caller needs to
// inspect the new config, it should do so inside onReload (typically by
// calling Source.Snapshot()).
func NewWatcher(path string, onReload func(), logger *slog.Logger) (*Watcher, error) {
	if path == "" {
		return nil, fmt.Errorf("watcher: path is required")
	}
	if onReload == nil {
		return nil, fmt.Errorf("watcher: onReload callback is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{
		path:     path,
		dir:      filepath.Dir(path),
		base:     filepath.Base(path),
		debounce: DefaultDebounceInterval,
		onReload: onReload,
		logger:   logger,
	}, nil
}

// SetDebounce overrides the debounce interval. Useful in tests.
func (w *Watcher) SetDebounce(d time.Duration) {
	if d > 0 {
		w.debounce = d
	}
}

// Run blocks until ctx is canceled or fsnotify fails fatally. It returns
// the terminating error (nil on clean ctx cancellation).
//
// Run watches the parent directory of the target path (rather than the file
// itself) so that editor atomic-save patterns — write to tmp, rename over
// target — still produce reload events.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	w.w = fsw
	defer func() { _ = fsw.Close() }()

	if err := fsw.Add(w.dir); err != nil {
		return fmt.Errorf("watch %s: %w", w.dir, err)
	}
	w.logger.Info("config.watcher.start", "path", w.path, "debounce_ms", w.debounce.Milliseconds())

	// Debounce: we keep a timer that resets on every relevant event, and
	// fire onReload when it finally expires.
	var timer *time.Timer
	fire := func() {
		w.onReload()
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			w.logger.Info("config.watcher.stop")
			return nil
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if filepath.Base(ev.Name) != w.base {
				continue
			}
			// Any relevant event on our config file bumps the debounce.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(w.debounce, fire)
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			// Errors are logged but do not abort the watcher — fsnotify
			// occasionally reports transient issues (inode churn etc).
			w.logger.Warn("config.watcher.error", "error", err.Error())
		}
	}
}

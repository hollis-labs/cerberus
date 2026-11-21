package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/cerberus/internal/opsref"
	"github.com/hollis-labs/cerberus/internal/opssink"
)

var (
	opsOnce sync.Once
	opsSink *opssink.Sink
)

// opsSettings reads the `ops:` section of events.yaml. A missing or unreadable
// file means the sink is off with no mapping.
func opsSettings() opsref.Settings {
	path, err := eventexport.DefaultSettingsPath()
	if err != nil {
		return opsref.Settings{}
	}
	s, err := eventexport.LoadSettings(path)
	if err != nil {
		slog.Default().Warn("ops_sink.settings_failed", "error", err.Error())
		return opsref.Settings{}
	}
	return s.Ops
}

// OpsMapping is the configured resource/project -> Ops identifier mapping. It
// is read from the settings file on each call, so an edit applies without a
// restart.
func OpsMapping() opsref.Mapping { return opsSettings().Map }

// openOpsSink builds the Ops sink once, when ops.enabled is set; otherwise nil.
func openOpsSink() *opssink.Sink {
	opsOnce.Do(func() {
		if s := opsSettings(); s.Enabled {
			opsSink = opssink.New(s, slog.Default())
		}
	})
	return opsSink
}

// WaitOps blocks, for at most d, until the deploy-outcome posts in flight have
// finished. A short-lived command calls it before it exits.
func WaitOps(d time.Duration) {
	if s := openOpsSink(); s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		s.Wait(ctx)
	}
}

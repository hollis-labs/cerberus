package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hollis-labs/libs/mesh/eventbus"
	"github.com/hollis-labs/libs/mesh/eventbus/relay"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// exportLoops are event export, started only when cerberus.event_export.enabled (off by default,
// plan section 4.2): the exporter, reading the audit and approvals logs in the data directory
// into its outbox there, and, when event_export.hub is set, the relay to the events hub. It is
// main's startEventExport with the settings from the cerberus block instead of events.yaml (which
// migrate-from transforms) and new's own data directory instead of ~/.cerberus. Nothing here reads
// or writes an events-tokens path.
//
// Like main, export never takes Cerberus down: a refusal to start is logged and the rest runs.
func exportLoops(st settings.Settings, dirs datadir.Dirs, logger *slog.Logger) []loop {
	cfg := st.EventExport
	if !cfg.Enabled {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	outbox := filepath.Join(dirs.Data, "events")
	exporter, err := eventexport.New(eventexport.Config{
		AuditDir: filepath.Join(dirs.Data, "audit"), ApprovalsDir: filepath.Join(dirs.Data, "approvals"),
		OutboxDir: outbox, Grace: time.Duration(cfg.GraceSeconds) * time.Second,
	})
	if err != nil {
		logger.Error("daemon.events_export.refused", "error", err.Error())
		return nil
	}
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	logger.Info("daemon.events_export.started", "generation", exporter.Status().Generation)
	loops := []loop{{name: "event_export", run: func(ctx context.Context) {
		stopped := map[string]string{}
		exporter.Run(ctx, interval, func(err error) {
			logger.Warn("daemon.events_export.step_failed", "error", err.Error())
		}, func(s eventexport.Status) {
			for name, src := range map[string]eventexport.SourceStatus{"audit": s.Audit, "approval": s.Approval} {
				if src.State == eventexport.StateStopped && stopped[name] != src.Reason {
					logger.Error("daemon.events_export.source_stopped", "source", name, "reason", src.Reason, "detail", src.Detail)
				}
				stopped[name] = map[bool]string{true: src.Reason, false: ""}[src.State == eventexport.StateStopped]
			}
		})
	}}}
	if cfg.Hub {
		if l := hubRelayLoop(exporter, filepath.Join(outbox, "hub"), logger); l != nil {
			loops = append(loops, *l)
		}
	}
	return loops
}

// hubRelayLoop ships the exporter's outbox to the event hub, found through the environment
// (HOLLIS_EVENTS_URL and the app's token file, as in mesh/eventbus); without a URL nothing
// starts. The relay moves its cursor only over events the hub settled, so a hub that is down
// holds events back rather than losing them; its last delivery attempt is the loop's flush.
func hubRelayLoop(exporter *eventexport.Exporter, dir string, logger *slog.Logger) *loop {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warn("daemon.events_hub.dir_failed", "error", err.Error())
		return nil
	}
	pub, err := eventbus.FromEnv(eventexport.AppName, eventbus.Config{
		SpoolDir: filepath.Join(dir, "spool"),
		Logger:   logger,
		OnError:  func(err error) { logger.Warn("daemon.events_hub.client", "error", err.Error()) },
	})
	if err != nil {
		logger.Error("daemon.events_hub.client_failed", "error", err.Error())
		return nil
	}
	deliverer, ok := pub.(relay.Deliverer)
	if !ok {
		logger.Info("daemon.events_hub.no_url", "env", eventbus.EnvURL)
		return nil
	}
	cursor := relay.FileCursor{Path: filepath.Join(dir, fmt.Sprintf("audit-%d.cursor", exporter.Status().Generation))}
	r, err := relay.New(relay.Config{
		Source:    eventexport.RelaySource{Outbox: exporter.Outbox()},
		Cursor:    cursor,
		Deliverer: deliverer,
		OnError:   func(err error) { logger.Warn("daemon.events_hub.relay", "error", err.Error()) },
	})
	if err != nil {
		logger.Error("daemon.events_hub.relay_failed", "error", err.Error())
		return nil
	}
	logger.Info("daemon.events_hub.started", "cursor", cursor.Path)
	return &loop{name: "events_hub", run: func(ctx context.Context) {
		if err := r.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("daemon.events_hub.stopped", "error", err.Error())
		}
	}, flush: func(ctx context.Context) {
		if err := pub.Close(ctx); err != nil {
			logger.Warn("daemon.events_hub.close", "error", err.Error())
		}
	}}
}

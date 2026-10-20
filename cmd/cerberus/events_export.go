package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/libs/mesh/eventbus"
	"github.com/hollis-labs/libs/mesh/eventbus/relay"
)

// startEventExport starts the event exporter when events.yaml enables it. It
// logs and carries on when export cannot start (a missing store, an
// inconsistent outbox): export is never a reason to take the daemon down.
func startEventExport(ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger) {
	settingsPath, err := eventexport.DefaultSettingsPath()
	if err != nil {
		return
	}
	settings, err := eventexport.LoadSettings(settingsPath)
	if err != nil {
		logger.Warn("daemon.events_export.settings_failed", "error", err.Error())
		return
	}
	if !settings.Export.Enabled {
		return
	}
	auditDir, auditErr := app.AuditDir()
	approvalsDir, approvalsErr := app.ApprovalsDir()
	home, homeErr := os.UserHomeDir()
	if auditErr != nil || approvalsErr != nil || homeErr != nil {
		logger.Warn("daemon.events_export.path_resolve_failed")
		return
	}
	exporter, err := eventexport.New(eventexport.Config{
		AuditDir: auditDir, ApprovalsDir: approvalsDir,
		OutboxDir: filepath.Join(home, ".cerberus", "events"), Grace: settings.Export.Grace(),
	})
	if err != nil {
		logger.Error("daemon.events_export.refused", "error", err.Error())
		return
	}
	logger.Info("daemon.events_export.started", "generation", exporter.Status().Generation)
	if settings.Export.Hub.Enabled {
		startHubRelay(ctx, wg, logger, exporter, home)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		stopped := map[string]string{}
		exporter.Run(ctx, settings.Export.Interval(), func(err error) {
			logger.Warn("daemon.events_export.step_failed", "error", err.Error())
		}, func(s eventexport.Status) {
			for name, src := range map[string]eventexport.SourceStatus{"audit": s.Audit, "approval": s.Approval} {
				if src.State == eventexport.StateStopped && stopped[name] != src.Reason {
					logger.Error("daemon.events_export.source_stopped", "source", name, "reason", src.Reason, "detail", src.Detail)
				}
				stopped[name] = map[bool]string{true: src.Reason, false: ""}[src.State == eventexport.StateStopped]
			}
		})
	}()
}

// startHubRelay ships the exporter's outbox to the event hub. The hub is found
// through the environment (HOLLIS_EVENTS_URL and the app's token file, as in
// mesh/eventbus); without a URL there is no hub and nothing starts. Like the
// exporter it is never a reason to take the daemon down: it logs and carries on.
//
// The relay reads the outbox, which is durable, and moves its cursor only over
// events the hub settled or the client durably quarantined, so a hub that is
// down or refuses the credential holds events back rather than losing them. The
// cursor and the client's spool live under ~/.cerberus/events/hub; the cursor
// file is named for the outbox generation, so an outbox that was recreated starts
// from its beginning rather than from a sequence number it never had.
func startHubRelay(ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger, exporter *eventexport.Exporter, home string) {
	dir := filepath.Join(home, ".cerberus", "events", "hub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warn("daemon.events_hub.dir_failed", "error", err.Error())
		return
	}
	pub, err := eventbus.FromEnv(eventexport.AppName, eventbus.Config{
		SpoolDir: filepath.Join(dir, "spool"),
		Logger:   logger,
		OnError:  func(err error) { logger.Warn("daemon.events_hub.client", "error", err.Error()) },
	})
	if err != nil {
		logger.Error("daemon.events_hub.client_failed", "error", err.Error())
		return
	}
	deliverer, ok := pub.(relay.Deliverer)
	if !ok {
		// No HOLLIS_EVENTS_URL: FromEnv returned the no-op publisher.
		logger.Info("daemon.events_hub.no_url", "env", eventbus.EnvURL)
		return
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
		return
	}
	logger.Info("daemon.events_hub.started", "cursor", cursor.Path)
	wg.Add(1)
	go func() { //nolint:gosec // G118: the final Close must outlive ctx, which is already done at shutdown
		defer wg.Done()
		if err := r.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("daemon.events_hub.stopped", "error", err.Error())
		}
		// ctx is already done at shutdown; the last delivery attempt needs its own.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := pub.Close(closeCtx); err != nil {
			logger.Warn("daemon.events_hub.close", "error", err.Error())
		}
	}()
}

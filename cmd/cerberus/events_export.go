package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/eventexport"
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

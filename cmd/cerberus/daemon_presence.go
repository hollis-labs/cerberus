package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/webui"
)

// newPresenceService is the daemon's passkey service (P3-4b): the key
// registry under the approvals directory, checked against the audit log's
// enrollment records, usable from the consoles running for this account.
func newPresenceService(logger *slog.Logger, approvalsDir string) *presence.Service {
	home, _ := os.UserHomeDir()
	var records []audit.Record
	if dir, err := app.AuditDir(); err == nil {
		if records, err = audit.ReadRecords(dir); err != nil {
			logger.Warn("daemon.presence.audit_read_failed", "error", err.Error())
		}
	}
	return presence.New(filepath.Join(approvalsDir, "passkeys"), app.AuditSink(), presence.Options{
		Records: records,
		Origins: func() []string { return webui.ConsoleOrigins(home) },
		Notify:  func(title, message string) { go notifyOperator(title, message) },
	})
}

// notifyOperator raises a macOS notification, best effort: the status line
// and the console header carry the same alert where this cannot.
func notifyOperator(title, message string) {
	if runtime.GOOS != "darwin" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := "display notification " + strconv.Quote(message) + " with title " + strconv.Quote(title)
	_ = exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).Run() //nolint:gosec // fixed binary; both strings are quoted as AppleScript literals
}

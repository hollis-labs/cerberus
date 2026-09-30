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
	"github.com/hollis-labs/cerberus/internal/userpresence"
	"github.com/hollis-labs/cerberus/internal/webui"
)

// newPresenceService is the daemon's passkey service (P3-4b): the key
// registry under the approvals directory, checked against the audit log's
// enrollment records, usable from the consoles running for this account.
func newPresenceService(logger *slog.Logger, approvalsDir string) *presence.Service {
	home, _ := os.UserHomeDir()
	var checked audit.Checked
	if dir, err := app.AuditDir(); err == nil {
		// Checked, not merely read (M4): an enrollment record past a break
		// in the chain does not vouch for the registry.
		if checked, err = audit.Check(dir); err != nil {
			logger.Warn("daemon.presence.audit_read_failed", "error", err.Error())
		} else if !checked.TailTrusted() {
			logger.Warn("daemon.presence.audit_untrusted", "problems", len(checked.Problems))
		}
	}
	return presence.New(filepath.Join(approvalsDir, "passkeys"), app.AuditSink(), presence.Options{
		Records: checked.Records,
		Trusted: checked.Trusted,
		Origins: func() []string { return webui.ConsoleOrigins(home) },
		Notify:  func(title, message string) { go notifyOperator(title, message) },
	})
}

// newUserPresence is the check the daemon raises itself before it allows a
// passkey enrollment (B1-b): cerberus-presence, next to this binary, which
// asks the person at the Mac through LocalAuthentication. Elsewhere, and
// without the helper, enrollment is refused with the recovery named.
func newUserPresence(logger *slog.Logger) userpresence.Verifier {
	if runtime.GOOS != "darwin" {
		return userpresence.Refuse{Why: "the person at the machine is asked only on macOS"}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return userpresence.Refuse{Why: "the daemon cannot find its own binary: " + err.Error()}
	}
	path := filepath.Join(filepath.Dir(exe), "cerberus-presence")
	return &userpresence.Helper{Path: path, OnPin: func(digest string) {
		logger.Info("daemon.user_presence.helper_pinned", "path", path, "sha256", digest)
		_, _ = app.AuditSink().Write(audit.Record{Kind: audit.KindPresenceHelper, Connector: "approvals", Operation: "presence_helper",
			Principal: audit.Principal{Kind: audit.PrincipalAutomation, Surface: "daemon", Via: "daemon"},
			Target:    audit.Target{Kind: "presence.helper", Fields: map[string]string{"path": path, "sha256": digest}},
			Note:      "the helper the daemon asks the person at the Mac through, as first used by this daemon", Posture: audit.PostureSecure})
	}}
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

---
id: "CERB-CAP-603"
class: "capability"
name: "Daemon install, uninstall and self-upgrade"
summary: "`install` and `uninstall` write and remove the launch agent, a runtime guard refuses to deploy the daemon through its own socket and names the manual recovery, and the installed plist gives the daemon a minimal PATH because the installer template omits EnvironmentVariables."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "installer template compared byte for byte against the live plist; the upgrade dance deliberately not performed"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "cmd/cerberus/cmd_install.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "install"
  - "upgrade"
  - "launchd"
  - "self-mutation"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-GAP-637"
    note: "the installer emits no PATH"
  - type: "relates_to"
    target: "CERB-GAP-636"
    note: "the upgrade dance is documented only as an error string"
  - type: "relates_to"
    target: "CERB-DEC-672"
    note: "the daemon is deliberately not Cerberus-managed here"
---

# Daemon install, uninstall and self-upgrade

`cerberus install` renders a fixed plist template to
`~/Library/LaunchAgents/com.fragments-engine.cerberus.plist`, pointing
`ProgramArguments` at the symlink-resolved path of the binary that was invoked,
and `launchctl load`s it. `cerberus uninstall` unloads and removes it. Both are
macOS-only and both use the deprecated `load`/`unload` verbs rather than
`bootstrap`/`bootout`, and both discard the unload error.

AGENTS.md describes the plist on this machine as hand-written. It is byte-for-byte
what `cerberus install` would have produced: same label, same
`ProgramArguments` of `[~/go/bin/cerberus, daemon, --foreground]`,
same `WorkingDirectory` of the home directory, same `RunAtLoad`, `KeepAlive` and
the two log paths. That distinction matters, because it relocates the cause of a
known outage. The daemon gets launchd's minimal `PATH` of
`/usr/bin:/bin:/usr/sbin:/sbin` not because someone wrote the plist by hand and
forgot, but because `launchdPlistTemplate` in `cmd/cerberus/cmd_install.go`
has no `EnvironmentVariables` key at all. `cerberus install` on a fresh machine
produces exactly the same environment. The contrast is sharp: the `os_service`
plist builder at `internal/connector/local/launchd.go:44` does emit
`EnvironmentVariables` for the resources it installs. The one process that most
needs a usable `PATH` — the daemon, which shells out to `go` for builds and
`docker` for the Docker connector — is installed by the only code path that
omits it.

The self-mutation guard is the strongest part of this capability.
`ProtectServingDaemon` records the serving executable and label at startup and
installs a mutation guard on the local connector; `refuseSelfMutation` then
refuses any deploy, apply, sync, reload, stop or remove that resolves to the
running daemon — matched by canonical resource id, by launchd label, by artifact
path for an `os_service` resource, or by `command[0]` for a dev_session one. It
does not merely refuse; it names the recovery in the error: build to a temporary
path, atomically move the binary over the daemon artifact, then
`launchctl kickstart -k gui/<uid>/com.fragments-engine.cerberus` from an external
terminal. That guard is generic enough to fire on this machine even though no
`cerberus-daemon-service` resource is registered here, because the `command[0]`
comparison would catch it.

So the upgrade path is documented, but only as a string in an error nobody has
hit yet, and only in AGENTS.md. There is no runbook, no `cerberus upgrade`
verb, and no test that the recovery instruction survives `redact.Text`, which
AGENTS.md explicitly requires of any error carrying a recovery. Running the
redactor over that exact message by hand shows it passes through intact — the
`-k` flag is not credential-shaped and `looksLikeToken` is not involved — so the
instruction is safe today by luck of wording rather than by assertion. This
audit did not perform the upgrade.

---
id: "CERB-CAP-954"
class: "capability"
name: "os_service runtime mode (systemd user units)"
summary: "Writes a systemd user unit for a process resource on Linux and drives it with systemctl --user, confirming a stable main PID before it calls the activation done — the Linux counterpart of the launchd backend."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.8
confidence_label: "unit-tested against a fake user manager, and exercised live once on a Linux box (torque deployed, inspected, stopped and removed; the daemon installed and reporting origin systemd); nothing runs under it long-term yet"
last_reviewed: "2026-09-30"
created_at: "2026-09-30"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/local/systemd.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "os_service"
  - "systemd"
  - "linux"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-100"
    note: "the os_service runtime mode on Linux"
  - type: "relates_to"
    target: "CERB-CAP-102"
    note: "the launchd backend it mirrors, guarantee for guarantee"
  - type: "depends_on"
    target: "CERB-CAP-103"
    note: "os_service + run_from: artifact installs through the artifact join"
  - type: "relates_to"
    target: "CERB-GAP-146"
    note: "closes it: systemd_user was recognised and refused before this"
  - type: "relates_to"
    target: "CERB-CAP-106"
    note: "the self-mutation guard names systemctl --user restart on Linux"
---

# os_service runtime mode (systemd user units)

`internal/connector/local/systemd.go` is the `os_service` backend on Linux, beside `launchd.go`, and `osServiceBackend` routes every method to it when `effectiveSupervisor` resolves `systemd_user` (which `auto` does on Linux). It deliberately has the launchd backend's shape, so what is true of one is true of the other.

**Naming.** A resource's unit is its launchd label with `.service`: `com.hollis-labs.cerberus.<project>.<resource>.service`, or `<service_name>.service` when the resource names its own. One resource has one name on every platform, and the daemon resource's `service_name: com.hollis-labs.cerberus` gives exactly the unit `cerberus install` writes. Units live in `~/.config/systemd/user`. `InstallLayout` carries `UnitName` and `UnitPath` beside `PlistPath`.

**Rendering.** `renderUnit` maps the plist onto a unit: `KeepAlive` is `Restart=always` with `StartLimitIntervalSec=0` (launchd never gives up on a crashing job, so neither does this), `RunAtLoad` is the unit being enabled under `default.target`, and `StandardOutPath`/`StandardErrorPath` are `StandardOutput=append:`/`StandardError=append:` into the same `~/.cerberus/apps/<project>/<resource>/logs/`. `env` is `Environment=`. `env_file` is `EnvironmentFile=-<path>`, with the file's digest in a comment, so a changed env file is a changed unit and apply restarts the service. Arguments are quoted, with `%` and `$` escaped, because systemd expands both where launchd passes arguments through; a relative program is anchored at the working directory and a bare name is resolved on the unit's PATH, since systemd searches neither. A newline in any value is refused. The unit is written 0600 and only when its bytes change.

**Environment.** A resource with no `PATH` in `env` gets the serving daemon's own PATH (`launchenv.Path`, the same composition `daemonLaunchPath` uses for the daemon), not the user manager's compiled-in one. Nothing else is set: the manager's environment, `DBUS_SESSION_BUS_ADDRESS` included, reaches the service, which is what go-keyring needs. A service whose environment (env or env_file) carries a secret reference is fronted by `cerberus run-secrets --`, as on launchd; `CheckSecretReferenceUnit` is the doctor's check that the unit on disk still does that.

**Lifecycle.** `Apply` syncs the artifact, writes the unit, runs `daemon-reload` only when the unit changed or the manager needs one, enables it if it is not, and restarts only when there is a reason — not active, artifact changed, unit changed, activation pending — and otherwise reports `noop`. It probes the port before starting a stopped service, through the same `checkServicePortConflict` the launchd backend uses. `Stop` stops the unit and leaves it enabled (a launchd bootout leaves the plist). `Reload` restarts an installed unit. `Remove` is `disable --now`, the unit file, a `daemon-reload`, `reset-failed` and the install tree.

**Startup confirmation.** `waitRunning` polls `systemctl --user show -p ActiveState,SubState,MainPID,…` until the unit is `active/running` with the same main PID twice, ten seconds by default, and on timeout says startup was not confirmed, that systemd may still retry, and to check `resource status` first — the launchd wording — with the unit's state and a journal tail attached.

**Status, inspect, doctor.** `Status` maps the unit onto Cerberus's states and never returns `(unknown, nil)`: not installed is `stopped`, `activating/auto-restart` after a non-zero exit is `failed` (crash-looping), and a state it does not recognise is `unknown` with an error that names it. `Inspect` parses `systemctl --user show` into a `SystemdRecord` and adds a short `journalctl --user` tail, both redacted: `redact.Systemd` hides the argument after a credential-named flag in the `ExecStart` record and reduces an `Environment=` line to names, then the per-resource redactor runs. `Environment` is not among the properties read at all. `resource doctor` checks the unit file, its load and enablement state, the exit status and diagnosis, and whether the user **lingers**: without `loginctl enable-linger`, user services stop at logout and do not start at boot.

**Tool resolution.** `systemctl`, `journalctl` and `loginctl` are resolved on every call, from PATH and then fixed system directories, never once at boot. Each call carries `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`, defaulted to `/run/user/<uid>` and its `bus` when the daemon was started without them.

**The daemon.** `cerberus install` on Linux writes `com.hollis-labs.cerberus.service` for the invoking binary with a composed PATH, retires a hand-written `cerberus.service` that runs `cerberus daemon` (and refuses one that does not), and says how to enable lingering rather than doing it. `daemon.SystemdSpawnedSelf` (INVOCATION_ID plus a cgroup under `user@<uid>.service`) makes `daemon status` report `origin: systemd`, and `daemon start|stop|restart` go through `systemctl --user` when the unit exists, as they go through launchctl on macOS.

Maturity is `partial`: the tests (`systemd_test.go`, `cmd_install_systemd_test.go`) run against a fake user manager and so pass on macOS CI, and the whole lifecycle has been driven once for real on one Linux host, but no service has run under it for long.

## What it owns

- unit rendering, escaping and idempotent write
- systemctl --user daemon-reload / enable / restart / stop / disable orchestration
- startup confirmation by stable main-PID observation (`waitRunning`)
- systemctl show parsing, state mapping, diagnosis, journal tail and redaction (`Inspect`, `redact.Systemd`)
- the lingering check in `resource doctor`
- the `cerberus run-secrets --` front for services whose env carries secret references
- the daemon's own unit (`cerberus install` / `uninstall` on Linux) and its systemd origin

## What it does not own

- restart on crash — systemd's `Restart=always` does that
- lingering itself: install and doctor name `loginctl enable-linger`, and neither runs it
- system-wide units, or any unit it did not write (a hand-written unit is retired only when it is the daemon's `cerberus.service`)
- the service's stdout/stderr, which go to the log files, not the journal
- resolution of the secret values themselves; the unit keeps references and `run-secrets` resolves them in the service process

## Vendor dependencies

- systemctl, journalctl, loginctl (systemd)

## Where it lives

- `internal/connector/local/systemd.go`
- `internal/connector/local/install_layout.go`
- `internal/connector/local/runtime.go`
- `internal/launchenv/launchenv.go`
- `internal/daemon/systemd_origin.go`
- `cmd/cerberus/cmd_install_systemd.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource apply|deploy|reload|stop|remove|status|inspect|doctor <id> on a mode: os_service resource on Linux; cerberus install|uninstall; cerberus daemon start|stop|restart|status.

## Recorded reasoning

- `docs/adr/0001-local-runtime-backends.md`
- `docs/adr/0002-resource-only-local-workload-model.md`

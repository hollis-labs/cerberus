---
id: "CERB-CAP-102"
class: "capability"
name: "os_service runtime mode (launchd)"
summary: "Writes a launchd user-agent plist for a process resource and drives it with launchctl bootstrap/bootout/kickstart, confirming a stable PID before it calls the activation done."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.8
confidence_label: "extensively unit-tested against a fake command runner; no resource on this machine uses os_service, so nothing here has a live exercise"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/local/launchd.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "os_service"
  - "launchd"
  - "macos"
  - "unproven"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-100"
    note: "the second runtime mode"
  - type: "depends_on"
    target: "CERB-CAP-103"
    note: "os_service + run_from: artifact installs through the artifact join"
  - type: "relates_to"
    target: "CERB-GAP-146"
    note: "only launchd is implemented; systemd_user and windows_service are refused at runtime"
  - type: "relates_to"
    target: "CERB-GAP-141"
    note: "the generated plist carries no PATH"
  - type: "relates_to"
    target: "CERB-GAP-148"
    note: "no os_service resource exists here to smoke-test it"
  - type: "relates_to"
    target: "CERB-CAP-106"
    note: "the self-mutation guard exists because the daemon is the canonical os_service resource"
---

# os_service runtime mode (launchd)

`internal/connector/local/launchd.go` is the `os_service` backend. `writePlist` syncs the artifact (when `run_from: artifact`), renders a plist with `RunAtLoad`, `KeepAlive`, explicit `StandardOutPath`/`StandardErrorPath` under `~/.cerberus/apps/<project>/<resource>/logs/`, and writes it to `~/Library/LaunchAgents/<label>.plist` only if the bytes changed. The label defaults to `com.fragments-engine.cerberus.<project>.<resource>`. `Apply` then reloads only when it has a reason to — not loaded, artifact changed, plist changed, or activation pending — and otherwise reports `noop`, so a repeated apply does not churn a healthy service.

The careful part is `waitRunning`. The comment above it states the constraint: command acceptance does not mean launchd could execute the binary. It polls `launchctl print` until it sees `state = running` with the *same* PID twice in a row, up to ten seconds, and on timeout returns an error that says startup was not confirmed, that `KeepAlive` may still retry, and that the operator should check `resource status` before retrying the operation. `bootstrapService` similarly handles the case where a just-removed job slot is still draining: it probes with `print` before attempting a recovery `bootout`, because booting out an already-absent job returns a misleading EIO.

`Inspect` parses `launchctl print` into a `LaunchdRecord` — loaded, state, PID, last exit code, throttled, reason — plus a human diagnosis and highlights, and runs the raw text through both `redact.Launchd` and the per-resource redactor before it is exposed. `resource doctor` turns that record into checks, and only into checks: the entire meaty half of the doctor is gated on `Mode == os_service`, which is why `doctor` on a dev_session resource reports one check (CERB-GAP-144).

Supervisor selection is `effectiveSupervisor`: `auto` resolves by GOOS to launchd on darwin, `systemd_user` on linux, `windows_service` on windows. Only launchd has a backend, so the other two resolve successfully and then fail with `os_service start not implemented yet`.

Maturity is `partial` deliberately. The unit coverage is real and thorough — `launchd_test.go` is 973 lines against an injected command runner — but `cerberus resource list` on this machine shows nine `dev_session` resources and zero `os_service` resources, and the daemon itself runs from a hand-written plist that Cerberus did not generate. The root CLI help says the daemon "now also fits this model as the v2 local process resource `cerberus-daemon-service`"; no such resource is registered here (CERB-GAP-149). Nothing in this capability is proven against a real launchd job by this installation.

## What it owns

- plist rendering and idempotent write (`writeFileIfChanged`)
- launchctl bootstrap / bootout / kickstart orchestration and the drain-slot retry
- startup confirmation by stable-PID observation (`waitRunning`)
- launchctl print parsing, diagnosis and redaction (`Inspect`, `redact.Launchd`)
- the `cerberus run-secrets --` front for services whose env carries secret references
- install-tree removal on `resource remove` (plist + install root)

## What it does not own

- restart on crash — launchd's KeepAlive does that, which is why the resource monitor skips os_service resources entirely
- systemd or Windows service supervision (recognised, not implemented)
- a PATH for the supervised service: `EnvironmentVariables` carries only env_file + env
- supervision of the daemon on this machine — that plist is hand-written and outside Cerberus
- resolution of the secret values themselves; the plist keeps references and `run-secrets` resolves them in the service process

## Vendor dependencies

- launchctl (macOS)

## Where it lives

- `internal/connector/local/launchd.go`
- `internal/connector/local/install_layout.go`
- `internal/connector/local/secret_plist.go`
- `internal/connector/local/runtime.go`
- `internal/daemon/launchd_origin.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource apply|reload|stop|remove|status|inspect|doctor <id> on a mode: os_service resource.

## Recorded reasoning

- `docs/adr/0002-resource-only-local-workload-model.md`

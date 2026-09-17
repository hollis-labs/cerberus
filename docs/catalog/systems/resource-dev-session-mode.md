---
id: "CERB-CAP-101"
class: "capability"
name: "dev_session runtime mode"
summary: "Runs a declared local command as a Cerberus-owned child process group, tracked by PID file plus a launch-identity check so it will never adopt or kill a process it did not start."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.95
confidence_label: "nine resources run this way on this machine right now; status, inspect, doctor and logs verified live"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/local/dev_session.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "dev_session"
  - "process"
  - "pid"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-100"
    note: "one of the two runtime modes the lane offers"
  - type: "relates_to"
    target: "CERB-CAP-104"
    note: "the resource monitor only restarts dev_session resources"
  - type: "relates_to"
    target: "CERB-GAP-143"
    note: "liveness is PID-based; declared health URLs are never probed"
---

# dev_session runtime mode

`dev_session` is the default mode and the only one in use on this machine. `internal/connector/local/dev_session.go` launches `command[0]` with `Setpgid: true`, writes stdout and stderr to one log file (`log_file:`, else `$TMPDIR/cerberus-<id>.log`), and records a PID file plus a meta file holding the PID, the launch time, a config hash and a `ps -o lstart` process-start identity.

That identity is the whole point of the design. `ownedPID()` refuses to adopt a PID whose recorded launch metadata is missing or whose OS start time disagrees with what Cerberus wrote, so a recycled PID cannot be mistaken for a supervised service. `stopContext` then refuses to signal a PID that is not its own process-group leader, rechecks ownership before escalating from SIGTERM to SIGKILL after ten seconds, and waits for the process to actually exit before a caller may restart it. The comment above `Stop` states the rule plainly: a listening port is evidence of occupancy, never evidence of ownership. When the port is held by something Cerberus does not own, `Poll` returns `unknown` with a `refusing to adopt or stop it` message rather than claiming the resource is running.

The child's environment is `os.Environ()` with `env_file:` and `env:` layered on, plus `ensurePath` appending `/opt/homebrew/bin`, `/opt/homebrew/sbin`, `/usr/local/bin`, `/usr/local/go/bin` and `~/go/bin` when they exist. That matters because the daemon's own PATH is launchd's minimal `/usr/bin:/bin:/usr/sbin:/sbin` — a dev-session child gets a usable PATH even though its parent does not. It does not, however, fix the daemon's own lookup of a build tool; see CERB-GAP-140.

The state vocabulary this mode produces is `running`, `starting`, `stopped`, `building` and `unknown`. It never produces `healthy` or `unhealthy` — nothing in the local connector emits those — so a resource declaring `health: http://...` gets no probe and `healthy` in the health DTO means only "the PID is alive".

The verbs map onto it directly: `apply` starts, `reload` stops then starts, `stop` pauses the resource in `pausectl` first so the monitor does not immediately restart it, `remove` is `stop` plus a pause, and `deploy` on a dev_session resource with no build strategy is `ActivateBuilt` — poll, stop if active, start again.

## What it owns

- child process launch with its own process group
- PID file, meta file and launch-identity verification (`ownedPID`)
- graceful stop with SIGTERM → SIGKILL escalation and ownership recheck
- combined stdout/stderr capture to one log file
- port-occupancy detection that refuses to adopt a foreign listener
- PATH augmentation for the child process (`ensurePath`)
- operator stop/resume via pausectl so a stop is not undone by the monitor

## What it does not own

- restart on crash — that is the resource monitor (CERB-CAP-104), not the session
- survival across a daemon restart: the session object is in-memory, and a restarted daemon re-adopts by PID file and identity rather than re-parenting
- artifact installation — `run_from: artifact` is meaningful only under os_service in practice
- health probing of the declared `health` / `health_check` fields
- log rotation or truncation: `os.Create` on every start truncates the previous log

## Vendor dependencies

- lsof
- ps

## Where it lives

- `internal/connector/local/dev_session.go`
- `internal/connector/local/runtime.go`
- `internal/connector/local/status_advice.go`
- `internal/service/service.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource apply|reload|stop|remove|logs|status <id>.

## Recorded reasoning

- `docs/adr/0002-resource-only-local-workload-model.md`

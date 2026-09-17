---
id: "CERB-CAP-104"
class: "capability"
name: "Auto-restart supervision"
summary: "A 30-second daemon loop that restarts down dev_session resources which opted into auto_restart, with per-resource attempt budgets, cooldowns, dependency ordering and an operator-stop override."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.85
confidence_label: "six resources are enrolled and running on this machine; I did not stop one to watch a restart, so the restart branch itself is test-verified rather than observed"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/resource_monitor.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "auto_restart"
  - "monitor"
  - "daemon"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-101"
    note: "only dev_session resources are monitored"
  - type: "implements"
    target: "CERB-CAP-100"
    note: "the active half of the supervision lane"
  - type: "relates_to"
    target: "CERB-GAP-142"
    note: "auto_start has no effect; auto_restart is the only enrolment switch"
  - type: "relates_to"
    target: "CERB-GAP-143"
    note: "restart is triggered by PID absence, never by a failed health probe"
---

# Auto-restart supervision

`internal/cerbapi/resource_monitor.go` is the only active supervision in the product. The daemon starts exactly one of these (`cmd_daemon.go` line 446) alongside the artifact drift cache; the v1 `daemon.Monitor` and `service.ServiceRegistry` are never constructed by the daemon at all.

Enrolment is `shouldMonitorResource`: local/process, mode resolves to `dev_session`, and `auto_restart: true`. `os_service` resources are excluded by design — launchd's `KeepAlive` already does that job, and a second restarter would fight it. On this machine six resources are enrolled (`postgres`, `jaeger`, `tether-daemon`, `tether-sysop`, `hadron-daemon`, `tesseract-api`, `torque-api` — seven, in fact) and three are deliberately not: `nanite-local` and `tunnel-muctlvaig` set both flags false because an auto-restarting `ssh` against a corporate auth endpoint is a good way to get an account locked out.

Each tick, the loop re-resolves config, garbage-collects retry state for resources that are no longer monitored (so a re-added id does not inherit an exhausted budget), orders resources by declared dependency, and for each enrolled resource: skips it if the operator paused it via `pausectl`, takes the runtime's `opMu` to read status, and treats `stopped`, `failed` or `unknown` as down. Before restarting it refuses on a port conflict, respects `max_restart_attempts` (default 3) and `restart_cooldown` (default 10s), logs the dependency warnings it is about to ignore, then calls `Apply` under `opMu`. A recovery resets the failure count and logs `resource_recovered`.

Two consequences are worth stating because they are easy to misread. First, the monitor's immediate `checkAllResources` at daemon boot means an `auto_restart: true` resource that is down gets started at boot — so `auto_restart` is effectively also the auto-start switch, and the actual `auto_start` field does nothing (CERB-GAP-142). Second, "down" means no owned PID. A process that is alive but wedged, or listening but returning 500s, is never restarted, because nothing probes the declared `health:` URL.

## What it owns

- the 30s supervision tick and the boot-time first pass
- enrolment by mode + auto_restart (`shouldMonitorResource`)
- per-resource failure budget, cooldown and retry-state garbage collection
- dependency-ordered iteration (`resourceStartupOrder`)
- operator-stop honouring via pausectl
- port-conflict refusal before restart
- structured restart_attempt / restart_success / restart_failed / max_restarts_exceeded logging

## What it does not own

- os_service resources — launchd KeepAlive owns those
- health-probe-driven restart: down means no owned PID
- auto_start semantics; the field is inert
- rebuilding before restart — it calls Apply, never Deploy, so a restart relaunches whatever binary is on disk
- restarting anything a human stopped: a pausectl pause wins until an apply/deploy/reload resumes it
- restarting the *daemon process* itself, which is `internal/daemon/restart.go` — live code, not v1 debt: `cerberus daemon restart` and `daemon --replace` both route through `daemon.RestartWithVerify` at cmd_daemon.go:258

## Where it lives

- `internal/cerbapi/resource_monitor.go`
- `internal/cerbapi/resource_dependencies.go`
- `internal/pausectl`
- `cmd/cerberus/cmd_daemon.go`

Surfaces: daemon. Entry points: cerberus daemon (automatic); cerberus resource stop <id> to opt out, apply/deploy/reload to opt back in.

## Recorded reasoning

- `docs/adr/0002-resource-only-local-workload-model.md`

---
id: "CERB-CAP-601"
class: "capability"
name: "Health reporting, monitoring and auto-restart"
summary: "The daemon polls local dev_session resources every 30s and restarts the ones that opted in, judging liveness from a PID file and a TCP port only — the configured health URLs are never fetched and nothing alerts a human."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "monitor and health paths read end to end and exercised live; the false-positive case could not be induced read-only"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/resource_monitor.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "health"
  - "monitoring"
  - "auto-restart"
  - "alerting"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-600"
    note: "monitors the estate this record describes"
  - type: "relates_to"
    target: "CERB-GAP-631"
    note: "health URLs never probed"
  - type: "relates_to"
    target: "CERB-GAP-632"
    note: "alerting is dead code on the frozen lane"
  - type: "relates_to"
    target: "CERB-GAP-635"
    note: "health silently omits unsupervised resources"
  - type: "relates_to"
    target: "CERB-CAP-100"
---

# Health reporting, monitoring and auto-restart

The live monitor is `ResourceMonitor` in `internal/cerbapi/resource_monitor.go`,
constructed once at `cmd/cerberus/cmd_daemon.go:446`. It ticks every 30 seconds,
walks the config snapshot in dependency order, and for each resource that is
`type: process`, `connector: local`, `mode: dev_session` and `auto_restart:
true` it asks the local connector for a status and calls `Apply` if the
resource is down. It respects the pause markers under `~/.cerberus/`, refuses
to restart into a port conflict, drops the retry budget for a resource that
stops being monitored, and warns about unavailable dependencies. That part works
and is visible in the log: `daemon.resource_monitor.restart_attempt`,
`restart_failed` and `resource_recovered` all appear in
`~/.cerberus/cerberus.log`.

Three things about it are not what the repo leads you to expect.

**Liveness is a PID file and a TCP dial. The `health:` URL is never fetched.**
Six of the nine local resources declare one — `jaeger`, `tether-sysop`,
`hadron-daemon`, `tesseract-api`, `torque-api`, `nanite-local`. The HTTP probe
machinery exists, in `internal/service/healthcheck.go`, with a URL check, a
command check, intervals and timeouts, and eight tests. `NewHealthChecker` has
no caller outside those tests. `devSession.Poll()` decides state from
`ownedPID()` and `findPIDByPort()`, and `ResourceHealth.Healthy` is then derived
from that state by `resourceStateHealthy`. So a process that holds its port and
answers 500 on `/health` reports `healthy: true`. The `health:` value survives
into the session config hash — so editing it counts as a config change — and is
read by `internal/pipeline/resolve.go`, but the supervision lane never issues
the request. On 2026-09-17 all six URLs happened to return 200 when curled
directly, so this is latent rather than currently wrong; it is correct by
coincidence, not by construction. `tether-daemon` is the sharpest case: it
listens on a unix socket, declares neither `port` nor `health`, and its
`healthy: true` rests entirely on the existence of a PID file.

**Nothing alerts anybody.** `ResourceMonitor` logs
`daemon.resource_monitor.max_restarts_exceeded` at WARN and stops. The
three-layer alerting that AGENTS.md and the code comments describe — structured
log, then a POST to the Volon API, then a fallback JSON file under
`~/.cerberus/alerts/` — lives in `internal/daemon/monitor.go`, which is bound to
the frozen v1 `service.ServiceRegistry`. `daemon.NewMonitor` has zero callers,
including zero test callers. The evidence that it once ran is still on disk:
`~/.cerberus/alerts/` holds exactly two files, for `hadron-daemon` and `jaeger`,
both timestamped 2026-09-15T13:25:39, and the log shows the matching
`daemon.monitor.alert.volon_failed` and `alert.file_written` pairs. Nothing has
been written there since the v2 migration on the same day. Even when it ran, its
only remote destination was a hardcoded `http://127.0.0.1:8085/v1/notifications`
belonging to Volon, an app that is not installed here and never was; there is no
configuration key, environment variable or schema entry anywhere in the repo for
an alert destination.

**Health silently drops the resource that is not local/process.**
`ResourceRuntimeService.Health` filters to `r.Type == process && r.Connector ==
local` before doing any work, so `muctlvaig` appears in none of the three health
surfaces — the `cerberus_health` MCP tool, the socket `/health`, or the
console's `/api/system`. Its sibling verbs honour the unsupervised contract
properly: `cerberus resource status muctlvaig` reports `unsupervised` with the
connector commands as its next step, and `cerberus resource doctor muctlvaig`
refuses with the same guidance. Only `health` omits it without saying so, and
the console's overview inherits the arithmetic: `resources: 10` beside `running:
8`, `stopped: 1`, `attention: 0`.

There is also no CLI verb here at all. `cerberus health` is an unknown command;
health is reachable only over MCP, the socket and the console.

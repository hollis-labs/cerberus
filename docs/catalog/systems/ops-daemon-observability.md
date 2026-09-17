---
id: "CERB-CAP-602"
class: "capability"
name: "Observability of the daemon itself"
summary: "One unrotated JSON log going back six months, a working 7-day counter-history snapshot recorder, launchd's stdout and stderr files, and no tracing of any kind."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "log contents, snapshot store and dependency tree all inspected directly"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/service/lifecycle_log.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "logging"
  - "observability"
  - "tracing"
  - "retention"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-GAP-633"
    note: "no rotation or retention on cerberus.log"
  - type: "relates_to"
    target: "CERB-GAP-643"
    note: "jaeger is supervised, not integrated"
  - type: "relates_to"
    target: "CERB-GAP-630"
    note: "the log is the only place an audit trail could come from, and it is too thin"
---

# Observability of the daemon itself

The daemon writes one JSON line per event to `~/.cerberus/cerberus.log`, set up
by `InitLifecycleLog` with `os.O_APPEND` and a plain `slog.NewJSONHandler`. On
this machine that file is 3050 lines and 385 KB, and its first line is dated
2026-03-22 — six months of history in a single file. There is no rotation
anywhere in the repo: no `lumberjack`, no size cap, no age-based truncation, no
logrotate fragment. The handler is constructed with a nil options struct, so the
level is the default and there is no way to turn debug on.

What the log contains is worth stating precisely, because it is the only
candidate for an audit trail. Of 3050 lines, 2621 are
`daemon.socket.request`, and every one of them carries exactly five fields:
`time`, `level`, `msg`, `method`, `path`. The rest are lifecycle and monitor
events: `service.start`, `service.exited`, `service.stop`, `service.kill`,
`daemon.lock.acquired`, `daemon.resource_monitor.*`, `daemon.drift_cache.*`,
`daemon.orphan_detected`, and the historical `daemon.monitor.*` family from
before the v2 migration.

Alongside it, `SnapshotRecorder` works and is the one piece of this capability
that is unambiguously shipped. It samples thirteen control-plane counters every
minute, persists them to `~/.cerberus/state/overview_snapshots.json`, and evicts
anything older than seven days on read and write. The file currently holds 1792
samples from 2026-09-15T17:31 to 2026-09-17T10:53 and is what the console's
overview trends render. It is also a useful historical record in its own right:
the growth from seven resources and five projects to ten and eight is visible in
it, and `registry_entries` is zero in every sample.

launchd captures the daemon's stdout and stderr to
`~/.cerberus/logs/launchd-std{out,err}.log`, as the plist directs. These are
also unrotated and also append across restarts — `launchctl print` reports 31
runs, and the stdout file is a flat list of "Cerberus daemon running (PID N)"
and "Shutting down..." pairs. The stderr file is the more useful of the two: it
is where a startup failure lands, and it currently records the 08:59 start
failing to restore the `contextforge` managed plugin because its directory had
moved, keeping the registration and naming the recovery, then loading it without
its credential.

Cerberus emits no traces. There is no `go.opentelemetry.io` entry in `go.mod`
and no case-insensitive match for `otel`, `opentelemetry` or `otlp` anywhere in
the Go source. `jaeger` is a registered resource because the applications
Cerberus supervises send spans to it; Cerberus supervises the collector and
health-checks its `health_check` extension on :13133, and that is the whole of
the relationship. Do not read the presence of the resource as integration.

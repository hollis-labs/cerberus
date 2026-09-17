---
id: "CERB-CAP-100"
class: "capability"
name: "Resource supervision lane"
summary: "Supervises long-lived local process resources through one shared runtime service that every operator surface calls, and reports every other resource kind as unsupervised rather than broken."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "read-only verbs run live against the running daemon; the os_service and artifact halves of the lane have no live exercise on this machine"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/resource_runtime_service.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "resource"
  - "local"
  - "process"
  - "runtime"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-101"
    note: "dev_session is the only runtime mode in use on this machine"
  - type: "depends_on"
    target: "CERB-CAP-102"
    note: "os_service/launchd is the second runtime mode"
  - type: "depends_on"
    target: "CERB-CAP-105"
    note: "reports non-local/process resources as unsupervised"
  - type: "implements"
    target: "CERB-DEC-161"
    note: "resources: is the only local workload model (ADR 0002)"
  - type: "relates_to"
    target: "CERB-DEC-160"
    note: "the lane is deliberately not widened to remote kinds"
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "the other half of the two-lane split"
---

# Resource supervision lane

`internal/cerbapi/resource_runtime_service.go` is the supervision lane. Every operator surface — the CLI, the daemon unix socket, the MCP tools, the web console — is a thin caller of it, and behaviour changes belong in it rather than in a caller. The lane owns one question: is this declared local process running, is the thing it runs current, and what should the operator do next.

Its scope is narrow on purpose. `SupervisedLocally(type, connector)` is `type == "process" && connector == "local"`, and that pair is hardcoded at 17 non-test sites across the repo — 8 of them inside `resource_runtime_service.go` itself, 12 inside `internal/cerbapi` (adding `unsupervised.go`, `resource_dependencies.go`, `drift_cache.go`, `resource_monitor.go`), and 5 further out in `internal/pipeline/resolve.go`, `internal/config/paths.go`, `internal/registry/ports.go`, `internal/registry/schema.go` and `cmd/cerberus/cmd_resource.go`. The code comment says "roughly ten places"; the real count is higher, and it is higher in the places that matter — the registry's port validation and the pipeline resolver share the same assumption, so widening the lane is not a one-file change.

A resource selects its runtime mode with `mode:`. `dev_session` (the default when `mode:` is absent) launches the command as a child process group and tracks it by PID file plus a launch-identity check. `os_service` writes a launchd plist and hands supervision to launchd. `run_from:` is orthogonal: `workspace` runs the command where it sits, `artifact` installs a copy under `~/.cerberus/apps/<project>/<resource>/bin/` and runs that. On this machine all nine local process resources are `dev_session` + `workspace`, none declares a `build_strategy`, and nothing reports an artifact state — so half of this capability's surface area, the half the CLI help text advertises most loudly, has no production exercise here at all.

The lane serializes mutations on a single `opMu`, which the background resource monitor also takes, so a monitor restart and an operator `apply` cannot interleave. Config is re-resolved from `~/.cerberus/config.yaml` on every call (`snapshotConfig`) rather than snapshotted at startup, which is why a newly registered project appears without a daemon restart and why an unregistered one vanishes immediately.

Every read path carries an answer rather than a bare state: `recommended_action`, `recommended_reason` and `recommended_next_step` name the verb to run, and `resource logs`/`inspect`/`deploy` output passes through a per-resource redactor built from the resource's own env before it reaches an operator.

## What it owns

- resolution of a resource id to a local process spec (`requireLocalProcessResource`, `SpecFromResourceConfig`)
- the per-resource lifecycle verbs: status, inspect, doctor, logs, apply, deploy, reload, stop, sync, remove, ensure-fresh
- serialization of mutations against the background monitor (`opMu`)
- on-demand config re-resolution and resolve diagnostics
- recommended-action advice and next-step text on every read path
- port-conflict refusal before any mutation (`refusePortConflict`)
- redaction of build, install, log and error output per resource

## What it does not own

- remote hosts and remote containers — those are the admin lane (`external_connector_service.go`)
- application-level health: it reports process liveness, never a probe result (see CERB-GAP-143)
- auto-start at daemon boot — `auto_start` is parsed and never acted on in this lane (see CERB-GAP-142)
- the frozen v1 `services:` lane and its `ServiceRegistry`, which the daemon never constructs
- the definitions themselves: it reads `~/.cerberus/config.yaml` and never writes it
- credentials — a resource names a secret reference and `cerberus run-secrets` resolves it in the service's own process
- supervision of the Cerberus daemon on this machine: the daemon runs from a hand-written plist and is not a registered resource

## Vendor dependencies

- launchctl (macOS, os_service mode)
- lsof (port occupancy probe)
- ps (launch-identity probe)
- git (artifact repo-state record only)

## Where it lives

- `internal/cerbapi/resource_runtime_service.go`
- `internal/cerbapi/unsupervised.go`
- `internal/cerbapi/resource_monitor.go`
- `internal/cerbapi/ensure_fresh.go`
- `internal/cerbapi/daemon_self_guard.go`
- `internal/connector/local/connector.go`
- `internal/connector/local/runtime.go`
- `internal/connector/local/spec.go`
- `cmd/cerberus/cmd_resource.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource <verb> <id>; daemon unix socket (SocketClient / socket_server.go); MCP tools cerberus_resource_*; web console (internal/webui).

## Recorded reasoning

- `docs/adr/0002-resource-only-local-workload-model.md`
- `docs/plans/infra-admin-control-plane.md`

---
id: "CERB-CAP-106"
class: "capability"
name: "Serving-daemon self-mutation refusal"
summary: "The serving runtime refuses any resource mutation that targets the Cerberus daemon running it, and names the out-of-band upgrade procedure instead."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "guard present in the installed binary's source and unit-tested; deliberately not exercised live because invoking it means invoking a mutation on the daemon"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/daemon_self_guard.go"
tags:
  - "cerberus"
  - "class:capability"
  - "safety"
  - "guard"
  - "daemon"
  - "self-upgrade"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-163"
    note: "never deploy the daemon through its own socket"
  - type: "implements"
    target: "CERB-CAP-100"
    note: "a refusal the lane applies to every mutation verb"
  - type: "relates_to"
    target: "CERB-CAP-102"
    note: "the daemon is the canonical os_service resource the guard protects"
---

# Serving-daemon self-mutation refusal

Deploying the Cerberus daemon through its own socket restarts the daemon mid-operation: the call dies on EOF, the artifact is left half-synced, and the launchd job can end up booted out where `KeepAlive` will not bring it back. `internal/cerbapi/daemon_self_guard.go` makes that unreachable.

`ProtectServingDaemon(executable, label)` is called once during daemon startup, before the monitor runs or any socket call is accepted, with `os.Executable()` and `XPC_SERVICE_NAME`. It sets the serving identity and — importantly — installs the same check as a `SetMutationGuard` on the local connector, so a pipeline that drives the connector directly without going through a transport client is covered too.

`refuseSelfMutation` decides "is this me" three ways: the resource id equals `cerberus-daemon-service`; the spec's `service_name` equals the serving launchd label or the canonical `com.fragments-engine.cerberus`; or the executable the resource would run is the same file as the serving executable. That last test is `sameExecutablePath`, which compares cleaned paths and then falls back to `os.SameFile`, so a symlinked or hardlinked path does not slip past. For `os_service` + `artifact` resources it compares against the install layout's artifact path; otherwise against `command[0]` resolved against the working dir.

The refusal is wired into `ReloadResource`, `StopResource`, `DeployResource`, `ApplyResource`, `SyncResource` and `RemoveResource`, and the error text is the recovery procedure: build to a temporary path, atomically move the binary over the daemon artifact, then `launchctl kickstart -k gui/<uid>/com.fragments-engine.cerberus` from an external terminal.

On this machine the guard is armed — `XPC_SERVICE_NAME=com.fragments-engine.cerberus` is in the daemon's environment and the serving executable is `~/go/bin/cerberus` — but no registered resource is the daemon, so nothing here has ever triggered it. Note the secondary consequence of the executable test: any `dev_session` resource whose `command[0]` is the cerberus binary would also be refused, which is correct but broader than "the daemon resource".

## What it owns

- serving-identity capture at daemon startup (`ProtectServingDaemon`)
- three-way self-identification: resource id, launchd label, executable identity
- inode-level executable comparison (`sameExecutablePath` / `os.SameFile`)
- refusal on reload, stop, deploy, apply, sync and remove
- the connector-level mutation guard covering non-transport callers such as pipelines
- the out-of-band self-upgrade instruction carried in the error

## What it does not own

- protecting a daemon that is not Cerberus-managed from being replaced by other means — `mv` over the binary still works, and is the sanctioned path
- the CLI's in-process fallback when the daemon is unreachable: with no serving daemon, `servingDaemon` is false and the guard is inert by design
- the rogue-manual-daemon check, which is a `resource doctor` check (`daemon_lock_origin`), not this guard
- preventing `launchctl` mutations issued directly by an operator

## Where it lives

- `internal/cerbapi/daemon_self_guard.go`
- `internal/cerbapi/daemon_self_guard_test.go`
- `internal/daemon/launchd_origin.go`
- `internal/connector/local/connector.go`

Surfaces: daemon, socket, mcp, console. Entry points: automatic on every resource mutation served by a daemon.

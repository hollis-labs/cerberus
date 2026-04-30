# Daemon Management V2 Plan

## Status

Active

## Date

2026-04-27

## Goal

Replace the current "PID files plus heuristics" approach for durable local services with an explicit dual-backend model:

- `dev_session` for development servers and repo-local workflows
- `os_service` for native supervisor-managed background services

TUI work is out of scope for this plan. The focus is CLI, API, HTTP, MCP, and the feature architecture beneath them.

## Current state summary

Today Cerberus has:

- a stronger daemon lock/restart path than before
- fresh-from-disk config reload through `ServiceRegistry`
- a v2 domain model and connector architecture
- a local process model still centered on `ManagedService`, PID files, and `lsof`
- a macOS-only daemon `install` command that writes a single launch agent for Cerberus itself

This means the system is structurally ready for the next step, but local durable service management is still on the old substrate.

## Active decisions

Locked in:

- dual local runtime backends
- native OS supervisors for durable services
- durable services should prefer installed artifacts in a Cerberus-owned user area
- new daemon-management features land in v2 config, not v1
- PID files remain dev-session-only, not the durable-service truth source
- `resources:` is the long-term local workload model; `services:` is compatibility only
- frontends, APIs, daemons, schedulers, and similar local workloads should converge on `process` resources with runtime policy (`dev_session` vs `os_service`)

Still to confirm as implementation details:

- final v2 field names for process runtime/install config
- exact per-platform artifact root layout under `~/.cerberus`
- whether Linux support starts with `systemd --user` only or also considers system scope later
- Windows implementation library choice

## Work plan

### Phase 1: Modeling and seams

- add ADR and active plan docs
- introduce typed local process v2 config for mode, supervisor, run source, and artifact metadata
- add a local runtime backend abstraction
- map current in-process supervision to a named `dev_session` backend

### Phase 2: macOS service backend

- implement a `launchd` adapter for process resources
- generate per-resource plists from typed process specs
- add install/update/remove flows for user launch agents
- query service status through `launchctl` rather than PID files

### Phase 3: artifact installation

- define installed-artifact layout in the Cerberus user area
- add sync/install manifest logic
- make service-mode resources run from installed artifacts, not workspace binaries
- detect stale installed artifacts versus current desired build/install state

### Phase 4: API and command surface

- expose runtime mode and supervisor info in CLI/API/MCP status
- route lifecycle operations through the correct backend
- add explicit commands for apply/install/reconcile where needed

### Phase 5: Linux and Windows

- add `systemd --user` backend
- add Windows service backend
- keep the backend contract aligned across platforms

## Immediate slice

The immediate slice in progress is:

1. Create canonical docs for the decision and plan.
2. Add typed local process spec support for new daemon-management fields.
3. Use that typed spec as the basis for the upcoming backend split.

## Risks to watch

- growing v2 config by accretion instead of introducing a clean typed process spec
- spending meaningful energy on preserving `services:` as a parallel first-class model
- keeping PID files as a hidden dependency for service-mode resources
- allowing service-mode resources to continue running from workspace binaries
- mixing dev-server ergonomics with durable-service guarantees

## Modernization alignment

This plan aligns with the portfolio modernization guide by:

- making architecture explicit in-repo
- reducing future ambiguity around execution and trust boundaries
- moving high-change daemon logic toward named ownership boundaries
- creating a reviewable path from current behavior to a durable control-plane model

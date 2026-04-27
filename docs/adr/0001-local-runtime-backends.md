# ADR 0001: Dual Local Runtime Backends For Process Resources

## Status

Accepted

## Date

2026-04-27

## Context

Cerberus is moving from a local process manager toward a broader control plane, but local `process` resources still run through a single in-process supervision path:

- spawn child process
- write PID and meta files
- poll with `kill -0`
- fall back to `lsof` on ports
- optionally wrap the Cerberus daemon itself in a macOS launch agent

That model is acceptable for fast local development workflows, but it is the wrong substrate for durable background services. It creates repeated issues around:

- stale PID files
- stale binaries after rebuilds
- ambiguity between repo-local outputs and user-owned runtime artifacts
- lack of an explicit distinction between dev servers and installed background services

At the same time, some project resources are development-facing (`vite`, `go run`, `wails dev`) while others should behave like long-running user services.

## Decision

Cerberus will support two local runtime backends for `process` resources:

1. `dev_session`
   Used for local iteration, repo-local commands, and short-feedback-loop workflows.

2. `os_service`
   Used for durable background services managed by the host supervisor.

The `os_service` backend maps to the native supervisor per platform:

- macOS: `launchd`
- Linux: `systemd --user`
- Windows: Service Control Manager

Cerberus will continue to own desired state, install state, and project orchestration, but it will stop treating PID files as the authoritative observed state for durable services. PID files remain a dev-session concern only.

## Implications

### Config model

V2 process resources need explicit fields for:

- runtime mode
- supervisor selection
- run source (`workspace` vs installed artifact)
- installed artifact path / service identity

### Build and install model

Cerberus must distinguish:

- building from source in a workspace
- installing a runtime artifact into a Cerberus-owned user area
- supervising a service from that installed artifact

For `os_service`, Cerberus should prefer user-owned runtime artifacts under `~/.cerberus` (or platform equivalent), not repo-local binaries or incidental `PATH` resolution.

### State model

Observed state for `os_service` should come from the OS supervisor first.

- `launchctl` on macOS
- `systemctl --user` on Linux
- SCM queries on Windows

Cerberus metadata and SQLite remain important for desired state, install manifests, and orchestration, but they are not the primary source of truth for whether a durable service is running.

### Compatibility

Existing v1 service definitions continue to map to the current dev-session behavior through migration and compatibility paths.

New daemon-management features land in v2 process resource config.

## Consequences

### Positive

- clean separation between dev and durable service workflows
- removes pressure to keep extending PID-file heuristics
- gives Cerberus a credible path to launchd/systemd/Windows service support
- makes stale-binary behavior explicit and fixable

### Negative

- adds another abstraction layer to the local process subsystem
- requires new config fields and migration guidance
- forces Cerberus to own artifact install/sync behavior for service-mode resources

## Initial implementation sequence

1. Capture the plan and ADR in-repo.
2. Introduce typed v2 process config for runtime mode and supervisor metadata.
3. Add a local runtime backend abstraction.
4. Keep current process supervision as the `dev_session` backend.
5. Implement `launchd` as the first `os_service` backend.
6. Add install-manifest and artifact-sync behavior.
7. Add `systemd --user` and Windows service backends.

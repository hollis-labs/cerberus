# ADR 0002: Resource-Only Local Workload Model

## Status

Accepted

## Date

2026-04-27

## Context

Cerberus historically exposed two local workload models:

- `services:` backed by `ManagedService`, PID files, daemon restart heuristics, and the original TUI
- `resources:` backed by the v2 domain model, connectors, runtime backends, daemon/socket API, and MCP

As v2 matured, the second model proved to be the correct long-term shape:

- local APIs, daemons, schedulers, frontends, and external tools all fit naturally as `process` resources
- runtime policy is better expressed as `dev_session` vs `os_service`
- the resource model already powers the modern operator surfaces

Cerberus has no external customers, very low migration risk, and no need to
spend product energy preserving two first-class local workload abstractions.

## Decision

Cerberus will treat `resources:` as the only future local workload model.

For local workloads:

- `resource` is the managed unit
- `type: process` is the local workload kind
- `mode: dev_session | os_service` is the runtime policy

`services:` is frozen immediately and becomes compatibility/deletion debt only.
It is no longer a first-class product surface.

The TUI is also frozen with the v1 lane. Future UI work will target the v2
resource-native model rather than extending the old TUI.

## Implications

### Operator surfaces

CLI, daemon, API, MCP, and future GUI work should converge on a single
resource-native execution layer.

The current shared execution target for v2 local resource operations is
`internal/cerbapi/resource_runtime_service.go`.

Legacy service-oriented commands remain only as transitional affordances and
should not gain new capabilities.

### Runtime model

All remaining local workloads should migrate into `resources:`:

- frontends and interactive loops -> `mode: dev_session`
- durable background services -> `mode: os_service`

### Code organization

Cerberus should move toward one shared application/service layer that owns
execution and lifecycle semantics. CLI, API, MCP, GUI, and any future clients
should be thin wrappers over that shared layer.

`internal/service/*` may continue to exist temporarily as implementation
plumbing for `dev_session`, but it is no longer the architectural center of
the product.

## Consequences

### Positive

- one local workload model instead of two
- lower operator confusion
- cleaner path to a future GUI
- easier deletion of legacy PID-file assumptions and split command surfaces

### Negative

- some legacy command paths become obviously transitional
- v1-oriented code will need consolidation or deletion
- temporary implementation layering may remain while `dev_session` still reuses parts of the old service package

## Follow-up direction

1. Freeze `services:` and the TUI.
2. Move all live local workloads into `resources:`.
3. Build a single resource-native application service layer.
4. Retire legacy service-oriented config and command paths.

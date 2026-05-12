# Cerberus Beta Sprint Execution

## Purpose

Turn the beta release plan into concrete sprint slices that can be executed by orchestrated subagents without re-planning from scratch every time.

This document is the execution companion to:

- [beta-release-plan.md](./beta-release-plan.md)

## Sprint 1: Runtime Core Stabilization

### Goal

Make the active v2 runtime path predictable enough that operators trust the CLI, daemon, API, and MCP outputs.

### Scope

- runtime truth and status correctness
- daemon/socket reliability
- explicit stop semantics
- launchd/operator diagnostics

### Tasks

- audit current runtime lifecycle semantics for `apply`, `deploy`, `reload`, `remove`, and internal pause behavior
- design and implement an explicit `resource stop` operator surface
- decide whether `pause` is a separate public verb or an internal implementation detail of `stop` for `dev_session`
- add CLI command, socket/API endpoint, shared runtime method, and MCP exposure for stop
- ensure stop semantics do not delete install state for artifact-backed services
- ensure stop semantics suppress unwanted auto-restart for `dev_session` resources until explicitly resumed/applied
- tighten launchd status reporting where healthy services still present as ambiguous or noisy
- add focused tests for stop/reload/remove/apply interactions
- verify daemon restart and reconnect behavior around the new stop semantics

### Definition Of Done

- operators can stop a resource without removing it
- `remove` is clearly destructive/uninstall-oriented
- `status` and `doctor` explain the stopped state clearly
- CLI/API/MCP parity exists for the active lifecycle verbs

## Sprint 2: Beta Operator Experience

### Goal

Make Cerberus usable by a new operator or agent from docs and built-in output alone.

### Scope

- quickstart
- troubleshooting
- help text and MCP descriptions
- resource modeling guidance

### Tasks

- write a macOS beta quickstart
- write a local-runtime troubleshooting guide
- write a concise “model your project” guide for `dev`, `uat`, and release-style resources
- audit root CLI help and `resource` subcommand help for beta clarity
- audit MCP descriptions for lifecycle, diagnostics, and logs
- tighten wording around `deploy` vs `apply` vs `reload` vs `stop` vs `remove`
- document standard conventions for:
  - `dev`
  - `uat`
  - release-style installed artifacts
  - ports
  - data roots
- ensure examples reflect the real commands and real resource shapes now in use

### Definition Of Done

- a fresh operator can install Cerberus, configure one project, and recover from common problems using repo docs and built-in output

## Sprint 3: Packaging And Release Process

### Goal

Make Cerberus itself releasable as a macOS beta without relying on repo-local usage as the primary operator path.

### Scope

- build artifact shape
- install path
- release checklist
- smoke verification

### Tasks

- define versioning and naming for beta artifacts
- define the canonical macOS binary release artifact shape
- document install path for released Cerberus binaries
- verify `cerberus install` and daemon bootstrap behavior against released binary locations
- define release checklist steps:
  - build
  - install
  - init
  - daemon bootstrap
  - resource smoke test
- create a release-candidate smoke checklist for local runtime flows
- verify the docs match the actual release path rather than just `go install`

### Definition Of Done

- a beta tag can be turned into a documented installable artifact and verified through a short repeatable checklist

## Sprint 4: Portfolio Validation

### Goal

Validate Cerberus beta against real project diversity and use that feedback to remove the last high-signal rough edges.

### Scope

- live portfolio coverage
- operator feedback
- final beta hardening

### Tasks

- keep Tangent running as a managed dev instance and verify MCP workflows
- validate one artifact-backed Go service
- validate one workspace-backed durable service
- validate one frontend/dev-server `dev_session`
- validate one desktop-adjacent dev workflow
- collect operator/agent feedback from real usage
- fix the highest-signal blockers discovered during validation
- update docs/examples/help text when the validation exposes ambiguity rather than just implementation bugs

### Definition Of Done

- Cerberus beta has daily-use validation across multiple local runtime shapes, with the remaining known issues explicitly called out rather than hidden

## Orchestrator Prompts

### Prompt 1: Runtime Core

```text
You are working in the Cerberus repo. Execute Sprint 1 of the beta plan: runtime core stabilization.

Primary objective:
- make the active v2 runtime path trustworthy

Required outcomes:
- audit current lifecycle semantics for deploy/apply/reload/remove and internal pause behavior
- add an explicit operator stop surface for v2 resources
- make stop semantics distinct from remove
- ensure artifact-backed services stop without uninstalling install state
- ensure dev_session stop semantics do not auto-restart until explicitly resumed/applied
- add CLI/API/MCP parity for the new stop behavior
- add focused tests around stop/reload/remove/apply interactions
- tighten any obviously misleading launchd status wording encountered along the way

Constraints:
- preserve the v2-only model
- do not reintroduce legacy service concepts
- favor the shared resource runtime service over duplicating behavior
- review and improve agent-facing output as part of the implementation

Deliverables:
- code changes
- tests
- concise summary of lifecycle semantics after the change
- list of follow-up gaps, if any
```

### Prompt 2: Operator Experience

```text
You are working in the Cerberus repo. Execute Sprint 2 of the beta plan: beta operator experience.

Primary objective:
- make Cerberus usable from docs and built-in output without hand-holding

Required outcomes:
- write a macOS beta quickstart
- write a troubleshooting guide for daemon/socket/launchd/artifact/runtime issues
- tighten the project-modeling guidance for dev, uat, and release-style resources
- audit CLI help text and MCP descriptions for lifecycle and diagnostics
- make deploy/apply/reload/stop/remove distinctions obvious to operators and agents
- ensure examples reflect the actual current runtime surface

Constraints:
- do not expand scope into GUI work
- keep the docs grounded in current shipped behavior
- prefer concise, operator-oriented docs over architectural essays

Deliverables:
- doc updates
- any small CLI/MCP wording changes needed to align with the docs
- concise summary of what a beta operator is expected to know and do
```

### Prompt 3: Packaging And Release

```text
You are working in the Cerberus repo. Execute Sprint 3 of the beta plan: packaging and release process.

Primary objective:
- make Cerberus itself releasable as a macOS beta product

Required outcomes:
- define beta artifact naming/versioning expectations
- define the canonical macOS install path for released Cerberus binaries
- verify or tighten cerberus install/bootstrap behavior against that release path
- write a release checklist and release-candidate smoke checklist
- ensure the install docs point at the release path, not only repo-local go install usage

Constraints:
- focus on Cerberus packaging, not app distribution for the wider portfolio
- keep the current macOS-first scope explicit
- avoid speculative cross-platform work in this sprint

Deliverables:
- docs/process changes
- any small code changes needed to make bootstrap/recovery align with released binary usage
- concise beta release checklist
```

### Prompt 4: Portfolio Validation

```text
You are working in the Cerberus repo. Execute Sprint 4 of the beta plan: portfolio validation.

Primary objective:
- validate the beta against real local project shapes and remove the highest-signal rough edges

Required outcomes:
- verify Tangent as a managed dev instance
- verify at least one artifact-backed Go service
- verify at least one workspace-backed durable service
- verify at least one frontend or dev-session workflow
- verify at least one desktop-adjacent workflow
- capture the highest-signal operator/agent feedback
- fix the issues that are true beta blockers
- update docs/help/output when the problem is discoverability rather than implementation

Constraints:
- do not sprawl into broad new feature work
- use real managed portfolio resources wherever possible
- distinguish true beta blockers from post-beta improvements

Deliverables:
- validation notes
- any code/docs fixes needed to remove beta blockers
- explicit list of remaining non-blocking issues for post-beta
```

# Cerberus Beta Release Plan

## Status

Draft

## Date

2026-05-11

## Goal

Define the minimum product, engineering, and release work required to ship a **Cerberus beta** for real local use without the future desktop GUI.

Execution detail now lives in:

- [beta-release-execution.md](./beta-release-execution.md)

For beta, Cerberus should be credible as:

- a macOS-first local runtime manager
- a resource-native CLI + daemon + MCP surface
- a stable operator tool for managing dev, UAT, and selected release-style local services

## Explicit Beta Scope

Included:

- v2 `resources:` local runtime
- CLI lifecycle and observability for local `process` resources
- daemon + socket API + MCP parity for active local runtime features
- macOS `launchd` durable service management
- artifact-backed deploy/apply/sync flows
- operator guidance and agent-facing help
- install/bootstrap path for Cerberus itself
- beta docs, examples, and release process

Excluded:

- desktop GUI polish and packaging as a primary surface
- Linux and Windows parity
- full app-distribution/install workflows for Tangent, Nanite, Nil, Hadron, etc.
- cloud-control-plane completeness across every connector
- release-grade updater/autoupdate flow

## Beta Product Definition

Cerberus beta is ready when a user on macOS can:

1. install Cerberus without cloning the repo just to operate it
2. initialize config and understand the v2 model quickly
3. run the daemon reliably on login via `launchd`
4. define and manage local `process` resources through CLI and MCP
5. use `deploy`, `apply`, `reload`, `logs`, `status`, `inspect`, and `doctor` without ambiguity
6. distinguish `dev` vs `uat` style resources clearly
7. recover from common failures using the built-in diagnostics and docs

## Beta Non-Goals

These should not block beta:

- perfect final naming of every config field
- first-class release install management for portfolio apps
- cross-platform supervisor support
- deletion of all dead legacy packages
- a polished end-user GUI

## Current Strengths

Cerberus already has the core beta substrate:

- v2-only local runtime lane
- shared runtime execution layer behind CLI, daemon/socket API, and MCP
- `dev_session` and `os_service` split
- macOS `launchd` backend
- artifact sync/install manifests
- `resource deploy` as the source-to-runtime verb
- artifact drift and repo-state drift guidance
- daemon and web UI resources dogfooded inside Cerberus itself

That means beta is mostly a **stabilization, documentation, installability, and operator-trust** release, not a greenfield feature release.

## Beta MVP Features

### 1. Runtime Management MVP

Must be solid:

- `resource list`
- `resource status`
- `resource inspect`
- `resource doctor`
- `resource logs`
- `resource stop` or equivalent explicit operator stop/pause path
- `resource deploy`
- `resource apply`
- `resource reload`
- `resource sync`
- `resource remove`

Must be true across:

- direct CLI fallback
- daemon/socket path
- MCP surface

### 2. Resource Modeling MVP

Beta should explicitly support:

- `dev_session` repo-backed resources
- `os_service` workspace-backed resources where needed
- `os_service` artifact-backed resources as the preferred durable path
- clear `dev` vs `uat` conventions in docs and examples

### 3. Install And Bootstrap MVP

Beta needs a simple path for Cerberus itself:

- installable binary artifact for macOS
- `cerberus init`
- `cerberus install` / `cerberus uninstall` as daemon bootstrap/recovery
- documented “first 10 minutes” setup that does not assume deep repo knowledge

### 4. Operator Diagnostics MVP

Beta should make common failures self-explanatory:

- launchd apply/bootstrap failures
- stale artifact vs stale repo-state distinction
- socket/daemon unavailable errors
- port conflicts for `dev_session`
- missing build artifacts
- confusion between stop, pause, remove, and reload semantics
- misrouted operator intent between `deploy`, `apply`, and `reload`

### 5. Agent-Facing Surface MVP

Beta should be easy for agents to use correctly:

- CLI help stays v2-first
- MCP descriptions stay verb-specific and lane-specific
- API/CLI status output suggests the next right action
- examples for common workflows exist in-repo

## Beta Release Criteria

Cerberus beta should not ship until these are true:

### Reliability

- daemon restart/recovery is stable under repeated use
- launchd-backed resources survive reboot/login cleanly
- `status` output is trustworthy enough for operators
- no known “success but actually stale/wrong binary” path remains in the beta workflow

### Documentation

- README reflects actual supported beta workflows
- one macOS quickstart exists for fresh users
- one local-runtime modeling guide exists for project owners
- one troubleshooting guide exists for the most common failures

### Packaging

- reproducible macOS binary release process is documented
- beta release artifact naming/versioning is defined
- install instructions reference released binaries, not just `go install`

### Validation

- one repeatable smoke suite for local runtime flows exists
- at least a small portfolio slice is managed successfully through Cerberus in real daily use
- Tangent, Clockwork, Hadron, Nanite, Nil, or equivalent projects provide real operator coverage

## Proposed Beta Sprints

### Sprint 1: Stabilize The Runtime Core

Goal:
- make the active v2 runtime path boring and trustworthy

Tasks:

- fix known runtime truth gaps and noisy status paths
- add an explicit operator stop surface for v2 resources, with semantics distinct from `remove`
- harden daemon restart/reconnect behavior
- tighten launchd state reporting where current output is misleading
- add or expand focused tests around deploy/apply/reload/status
- resolve any remaining “daemon reachable vs unreachable” rough edges in CLI/MCP

Definition of done:

- repeated daemon/resource lifecycle operations behave predictably
- no known sharp edge where operator intent regularly produces the wrong outcome

### Sprint 2: Beta Operator Experience

Goal:
- make the CLI/MCP/docs sufficient for an operator or agent without hand-holding

Tasks:

- produce a macOS quickstart
- produce a “model your project for Cerberus v2” guide with concrete examples
- produce a troubleshooting guide for daemon/socket/launchd/artifact issues
- audit CLI help, MCP tool descriptions, and status wording again with beta in mind
- define and document standard tags/conventions for `dev`, `uat`, and release-style resources

Definition of done:

- a new operator can install, boot, and manage one project from docs alone

### Sprint 3: Beta Packaging And Release Process

Goal:
- make Cerberus itself releasable as a beta product

Tasks:

- define versioning and release checklist
- define macOS binary build artifact shape
- document install path for released Cerberus binaries
- validate daemon bootstrap/install against released binary paths
- write beta smoke-test checklist for release candidates

Definition of done:

- a tagged beta can be built, installed, verified, and announced from a documented process

### Sprint 4: Beta Portfolio Validation

Goal:
- prove the beta against real workload diversity

Tasks:

- keep Tangent managed as a dev instance
- validate at least a few different runtime shapes:
  - artifact-backed Go API
  - workspace-backed durable service
  - `dev_session` frontend/dev server
  - desktop-adjacent dev workflow
- collect agent/operator feedback and fix the highest-signal issues

Definition of done:

- Cerberus beta has real daily-use evidence across multiple project shapes

## Suggested Beta MVP Task List

The likely concrete tasks to pull first:

1. Fix daemon/launchd status quirks that make healthy services look ambiguous.
2. Add an explicit `stop`/pause operator surface that does not delete install state.
3. Add a beta quickstart and troubleshooting docs.
4. Define a release build/install path for Cerberus itself that does not depend on repo-local usage.
5. Create a repeatable runtime smoke checklist for release candidates.
6. Audit MCP and CLI copy one more time for beta discoverability.

## After Beta

These are the likely next priorities once beta is shipped:

### 1. Canonical App Release Management

- pilot `dev` vs `release` split with one app, likely Nanite
- manage installed user-facing artifacts outside the repo
- define app install/update workflows for portfolio apps

### 2. Linux Support

- add `systemd --user`
- validate parity for the active runtime verbs

### 3. Windows Support

- define Windows service backend and install story

### 4. GUI/Desktop Surface

- desktop app becomes the richer operator surface once the backend contract is stable

### 5. Deeper Control Plane Expansion

- expand non-local connectors and higher-level orchestration once the local runtime product is stable

## Key Beta Decision

For beta, Cerberus should market itself as:

- **macOS-first**
- **agent-first**
- **local-runtime focused**

It should not pretend to be a finished cross-platform universal control plane yet. The beta should prove the local runtime model, operator experience, and agent experience first.

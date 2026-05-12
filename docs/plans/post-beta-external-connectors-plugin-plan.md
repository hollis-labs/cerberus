# Post-Beta External Connectors And Plugin Plan

## Goal

After beta, keep Cerberus beta work focused on local resource management while
the post-beta lane expands external service integrations, deployments, Docker,
and connector packaging.

External connectors should be designed so they can run in two forms:

1. Built into a Cerberus binary at compile time.
2. Packaged as subprocess plugins using `github.com/hollis-labs/plugin-sdk`.

The compile-time path should come first. It gives us stable contracts and
testability without committing every connector to a runtime plugin boundary
immediately.

## Current State

Cerberus already has the right internal shape for this work:

- `internal/domain/connector.go` defines a small `Connector` interface with
  resource lifecycle methods and capabilities.
- `internal/connector/registry.go` holds connector registration and lookup.
- Connectors are already split by provider under `internal/connector/*`.
- Several connectors already separate facade logic from backend logic, such as
  GitHub API/CLI backends and Docker CLI backend.

The extraction blockers are mostly boundary issues:

- Connector contracts live under `internal/`, so other apps and external
  modules cannot import them.
- CLI and MCP tools are hand-registered in Cerberus command code rather than
  derived from connector metadata.
- Some connectors expose MCP-specific JSON helpers beside provider operations.
- The daemon-facing resource runtime service is still heavily focused on local
  process resources.
- Config is mostly `map[string]any`; external connectors need declared config,
  validation, and secret requirements.

## Nanite Plugin-SDK Fit

Nanite uses `github.com/hollis-labs/plugin-sdk` v0.3.0. The SDK is a good fit
for post-beta external connector plugins because it already provides:

- Subprocess plugin lifecycle: `Init`, `Load`, `Unload`, health checks.
- JSON-RPC over stdio with panic recovery and host-managed dispatch.
- Manifest-driven config, including `secret` fields and environment variable
  resolution.
- Declarative MCP, CRUD, HTTP, event, and command registrations through
  `plugin.yaml`.
- Packaging, checksums, signatures, catalog metadata, and install-time trust
  checks.
- Persistent data and cache directory injection per plugin.

The SDK is not a drop-in replacement for Cerberus connectors. Its built-in
`plugin.Connector` type is for outbound send-style integrations, not resource
lifecycle management. Cerberus should define a host-specific connector
contract on top of the SDK, then adapt that contract to the SDK's subprocess
MCP, CRUD, health, and config surfaces.

Avoid Go's standard `plugin` package for this work. It is a poor fit for
portable, signed, cross-platform distribution. The SDK's subprocess model is
the better runtime plugin boundary.

## Recommended Architecture

### 1. Public Connector Contract

Create a public package or module for the connector contract, for example:

```text
pkg/cerbkit/connector
pkg/cerbkit/resource
```

The public contract should include:

- Connector identity and version.
- Resource types owned by the connector.
- Capabilities per operation.
- Typed operation requests and responses for create, start, stop, destroy,
  status, logs, build, deploy, and health.
- Config schema and secret requirements.
- Tool metadata: names, descriptions, examples, input schemas, destructive
  flags, and dry-run support where applicable.

This makes connectors importable as normal Go packages and gives agents better
tool discovery before runtime plugins exist.

### 2. Build-Time Connector Packages

Move provider implementations toward packages that can be included at build
time. Cerberus would register built-ins through simple registration functions:

```go
registry.Register(docker.New(...))
registry.Register(github.New(...))
```

This is the lowest-risk path and should be the first post-beta implementation
slice. It lets us validate the contract with real connectors before committing
to subprocess adapter details.

### 3. Manifest-Driven CLI, API, And MCP Adapters

Stop treating CLI/MCP responses as separate bespoke surfaces per connector.
Connector metadata should drive:

- CLI command descriptions and examples.
- MCP tool names, descriptions, schemas, and safety annotations.
- API capability discovery.
- GUI action availability.

Hand-written command handlers can remain, but agent-facing metadata should be
centralized so every new connector ships with good discovery output by default.

### 4. Plugin-SDK Adapter

Once the public connector contract is stable, add a Cerberus plugin host
adapter that maps SDK capabilities to Cerberus connector operations:

- `plugin.yaml` declares the connector id, resource types, config, secrets,
  MCP tools, CRUD resources, and optional HTTP routes.
- SDK `HealthChecker` maps to connector health.
- SDK `MCPHandler` maps to tool-level connector operations.
- SDK `CRUDHandler` can back resource inventory or provider-native resource
  objects where CRUD semantics are natural.
- Cerberus daemon owns plugin process supervision, config resolution, secrets,
  registration, status, and unload.

The Cerberus-specific connector declaration may initially live under
`plugin.yaml` metadata until the SDK has a first-class host-extension field.

## Lift Estimate

### Low To Medium: Compile-Time Package Readiness

Estimated at one sprint.

Work:

- Move stable connector contracts out of `internal/`.
- Remove Cerberus-specific presentation helpers from provider packages.
- Add connector metadata and config schema declarations.
- Keep existing built-in connector behavior working.

Risk:

- Mostly import churn and contract naming. Low runtime risk if provider
  implementations stay in place.

### Medium: Generic External Connector Runtime

Estimated at one sprint.

Work:

- Add a generic external connector operation service in the daemon.
- Route CLI, API, and MCP external connector operations through that service.
- Add consistent structured errors, stale/unavailable detection, and
  capability discovery.

Risk:

- Current local runtime service is intentionally local-process specific. Do not
  force remote providers into that service; add a parallel connector operation
  layer.

### Medium To High: Plugin-SDK Runtime Support

Estimated at two to three sprints after the public contract exists.

Work:

- Add plugin install/load/unload/config/secrets lifecycle to Cerberus or reuse
  the Nanite host pieces where appropriate.
- Implement a Cerberus adapter for plugin-sdk subprocess plugins.
- Define connector metadata in manifests.
- Add trust, signing, and developer-mode behavior.
- Pilot one connector as a subprocess plugin.

Risk:

- Secrets, destructive infrastructure operations, and process lifecycle need a
  stricter policy model than simple read-only plugins.
- SDK MCP tool listing is manifest-authoritative, so generated manifests and
  connector metadata must stay in sync.

## First Pilot

Use Docker as the first post-beta connector pilot.

Reasons:

- It is central to the post-beta direction.
- It is local and easy to test without cloud credentials.
- The existing connector is CLI-backed and already has clear operations.
- It exercises lifecycle, logs, status, compose up/down, and stale detection.

Do GitHub second. It is useful for release/deployment flows, but auth and
remote API behavior add more policy complexity.

## Proposed Post-Beta Sprints

### Sprint PB-1: Connector Contract Extraction

Outcome: external connectors can be authored as normal Go packages and
included at build time.

Tasks:

- Create public connector/resource packages.
- Move stable connector types out of `internal/domain`.
- Add config schema and secret requirement declarations.
- Add operation metadata for CLI/MCP/API descriptions.
- Convert Docker and GitHub to use the public contract while still built in.

### Sprint PB-2: External Connector Operation Service

Outcome: daemon-owned external connector operations with consistent API, CLI,
and MCP behavior.

Tasks:

- Add daemon service for external connector operations.
- Route existing Docker, GitHub, Cloudflare, Forge, Namecheap, and SSH tools
  through the service where practical.
- Add structured stale/unavailable/credential-missing errors.
- Expose capability discovery through API and MCP.

### Sprint PB-3: Plugin-SDK Host Adapter

Outcome: Cerberus can install and run a subprocess connector plugin.

Tasks:

- Add plugin install/load/unload/config lifecycle.
- Map plugin-sdk health and MCP calls to Cerberus connector operations.
- Add connector manifest metadata.
- Add trust/developer-mode policy.
- Build a Docker connector plugin prototype.

### Sprint PB-4: Deployment Connectors

Outcome: post-beta deployment workflows start using the same connector system.

Tasks:

- Add Docker deployment workflows on the connector contract.
- Add GitHub release/workflow operations.
- Add Cloudflare deployment/DNS operations behind explicit safety metadata.
- Add agent-facing examples and dry-run output for destructive operations.

## Non-Goals For Beta

- Runtime plugin install/uninstall for external connectors.
- Cloud deployment orchestration.
- Replacing local resource management.
- Shipping a public connector marketplace.

## Decision

Post-beta should use the plugin-sdk, but only after Cerberus first extracts a
stable connector contract and proves it through build-time packages. The SDK
should be treated as the packaging, process-isolation, lifecycle, and manifest
layer, not as the domain connector contract itself.

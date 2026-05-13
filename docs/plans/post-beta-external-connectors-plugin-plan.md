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

Create a public package or module for the connector contract:

```text
pkg/connector
pkg/resource
pkg/secret
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

- Create public connector/resource packages. *(Done: `pkg/connector`,
  `pkg/resource`, and `pkg/secret` now hold the importable connector,
  capability, resource, state, and secret provider contracts.)*
- Move stable connector types out of `internal/domain`. *(Done:
  `internal/domain` now aliases the public types so existing code keeps working
  while new code can import the public packages.)*
- Add config schema and secret requirement declarations. *(Done for PB-1:
  `pkg/connector.ConfigSchema` declares config fields and secrets; GitHub now
  declares repository fields and token secret metadata.)*
- Add operation metadata for CLI/MCP/API descriptions. *(Done for PB-1:
  `pkg/connector.Operation` defines shared operation metadata; Docker and
  GitHub now expose built-in connector definitions; the connector registry can
  now return sorted definitions for discovery adapters, including static
  definitions for connectors that are not available as live instances; the
  `connectors` CLI command lists this metadata.)*
- Convert Docker and GitHub to use the public contract while still built in.
  *(Done: Docker now satisfies the public connector lifecycle contract for
  container and compose resources while retaining its existing CLI methods;
  GitHub now explicitly satisfies the same public contract; app startup now
  registers their discovery metadata and live connectors when available;
  Docker and GitHub lifecycle signatures now use public resource, connector,
  and secret contracts rather than `internal/domain`.)*

### Sprint PB-2: External Connector Operation Service

Outcome: daemon-owned external connector operations with consistent API, CLI,
and MCP behavior.

Tasks:

- Add daemon service for external connector operations. *(Done for PB-2:
  `internal/cerbapi.ExternalConnectorService` now executes Docker and GitHub
  operations through the connector registry without using the local-process
  runtime service; the daemon socket exposes
  `POST /connectors/{id}/operations/{operation}`.)*
- Route existing Docker, GitHub, Cloudflare, Forge, Namecheap, and SSH tools
  through the service where practical. *(Done for Docker/GitHub: CLI and MCP
  tools now route through `ExternalConnectorService`; the service can be
  constructed without requiring a Cerberus config file. Cloudflare, Forge,
  Namecheap, and SSH remain direct adapters until PB-3/PB-4 safety and
  connector-definition policy is in place.)*
- Add structured stale/unavailable/credential-missing errors. *(Done for PB-2:
  unavailable, credential-missing, unsupported-operation, and invalid-args
  errors now use structured error codes; constructor errors are retained for
  unavailable connectors.)*
- Expose capability discovery through API and MCP. *(Done: the daemon socket
  now exposes `GET /connectors`, `cerbapi.Client` has `ListConnectors`, and MCP
  registers `cerberus_connector_list` against the same daemon-backed client.)*

### Sprint PB-3: Plugin-SDK Host Adapter

Outcome: Cerberus can install and run a subprocess connector plugin.

Tasks:

- Add plugin install/load/unload/config lifecycle. *(Started:
  `internal/pluginhost.Host` defines the install/load/unload/health/execute
  boundary without binding the rest of Cerberus to plugin-sdk types.
  `internal/pluginhost.DirectoryInstaller` now installs from a local plugin
  directory or `plugin.yaml` path, reads and validates `plugin.yaml`, applies
  trust-policy checks, and returns an `InstalledPlugin` record carrying the
  validated manifest, spec, and trust decision. `cerberus connectors plugin
  health <dir>` and `cerberus connectors plugin exec <dir> <operation>` now
  drive that lifecycle through `pluginhost.Manager` for local plugin
  directories with explicit trust inputs. The daemon `InProcessClient` and
  socket API now expose the same plugin health/exec path through
  `cerbapi.PluginConnectorService`. A daemon-scoped
  `cerbapi.ManagedPluginConnectorService` now also supports in-memory
  install/load/unload/list/health/exec for plugins that should stay loaded
  across requests. `cerberus connectors plugin managed ...` now exposes that
  daemon-managed lifecycle over the socket for operators. Managed plugin
  install/load state is now persisted under `~/.cerberus/plugin-connectors.json`
  and restored on daemon startup. Loaded managed plugins now also participate
  in normal connector discovery and `ExecuteConnectorOperation` routing, so
  plugin connectors can behave like first-class external connectors instead of
  living only behind plugin-specific commands. The operator-facing
  `cerberus connectors` command now prefers the daemon connector inventory,
  which makes managed plugin connectors visible from the normal CLI when the
  daemon is running, while still falling back to local built-in discovery when
  the daemon is unavailable. Catalog/archive installation and richer plugin
  inventory metadata still remain out of scope for this slice.)*
- Add a subprocess manager for plugin lifecycle and supervision. *(Started:
  `internal/pluginhost.Manager` now owns installed-plugin registration,
  process launch, init/load/unload sequencing, health checks, operation
  execution, and trust-tier enforcement for loaded plugins. Health polling,
  graceful shutdown semantics, and the concrete plugin-sdk RPC transport
  remain to be added. `internal/pluginhost.SubprocessLauncher` now resolves
  validated relative entrypoints into executable paths inside the plugin
  directory, rejects non-executable targets, and launches subprocess commands
  with an explicit environment allowlist instead of inherited ambient env.)*
- Map plugin-sdk health and MCP calls to Cerberus connector operations.
  *(Started: `internal/pluginhost.StdioTransportFactory` now speaks the
  plugin-sdk JSON-RPC stdio protocol for `plugin/init`, `plugin/load`,
  `plugin/unload`, `plugin/health`, and `mcp/call_tool`. The transport is
  exercised end-to-end against a test helper process running the real
  `plugin-sdk/subprocess.Serve` server. Full daemon wiring and a production
  plugin process supervisor still remain.)*
- Add connector manifest metadata. *(Done for PB-3 foundation:
  `pkg/connector.Manifest` is generated from connector definitions and validates
  resource types, config, secrets, operations, and destructive-operation
  acknowledgments. `internal/pluginhost.PluginYAML` embeds that manifest under a
  Cerberus-specific section.)*
- Add trust/developer-mode policy. *(Done for PB-3 foundation:
  `internal/pluginhost.TrustPolicy` requires signed catalog + archive signatures
  and SHA-256 by default, only allows unsigned local plugins in `devmode` builds,
  fails hard when requested sandboxes are not enforced, and blocks destructive
  agent-auto execution for dev plugins.)*
- Tighten plugin entrypoint and environment handling. *(Done for PB-3
  foundation: plugin metadata uses structured `entrypoint.command` +
  `entrypoint.args`; commands must be relative paths inside the plugin
  directory; shell-string parsing and PATH lookup are rejected by default.
  Plugin manifests declare logical secrets and config, while Cerberus resolves
  secrets itself instead of honoring arbitrary manifest-selected env vars.)*
- Build a Docker connector plugin prototype. *(Started:
  `internal/plugins/dockerplugin` now wraps the existing Docker connector as a
  `plugin-sdk/subprocess` plugin with health checks and MCP tool handlers for
  `list_containers`, `logs`, `start`, `stop`, `destroy`, and `status`.
  `cmd/cerberus-docker-plugin` serves that plugin as a standalone subprocess
  binary. `internal/plugins/dockerplugin.WritePrototype` now generates a
  prototype plugin directory with a manifest-derived `plugin.yaml`, and
  `cerberus connectors write-plugin-prototype docker <dir>` exposes that path
  from the CLI. `--build-binary` now stages a built
  `bin/cerberus-docker-plugin` executable into that prototype directory.
  Daemon installation wiring still remains.)*

PB-3 trust notes from Nanite / plugin-sdk / Agent Mux / Clockwork review:

- `plugin-sdk` is host-neutral protocol/lifecycle infrastructure; Cerberus owns
  the `plugin.yaml` schema and connector manifest section.
- Production installs should enforce SHA-256, catalog signature, and archive
  signature. Unsigned local plugins should require a dev build plus explicit
  allow-list roots.
- Plugin entrypoints should be structured as command + args, stay inside the
  plugin directory, and avoid shell-string parsing or PATH lookup by default.
- Secrets should be resolved through Cerberus declared secret names, not by
  allowing arbitrary manifest environment-variable reads.
- Sandbox profiles should fail closed when requested but unavailable.
- Docker plugin access is privileged because Docker socket access is effectively
  host-level control; require signed trust or explicit dev-mode operator intent.

### Sprint PB-4: Deployment Connectors

Outcome: post-beta deployment workflows start using the same connector system.

Tasks:

- Add Docker deployment workflows on the connector contract.
- Add GitHub release/workflow operations.
- Add Cloudflare deployment/DNS operations behind explicit safety metadata.
- Add Namecheap domain/DNS operations on the same contract. *(Started:
  Cloudflare and Namecheap now expose public connector definitions and are
  registered as built-in discovery metadata even when unavailable. The
  daemon-owned `ExternalConnectorService` now supports Cloudflare DNS
  operations (`list_zones`, `list_dns_records`, `create_dns_record`,
  `delete_dns_record`) and Namecheap domain/DNS operations (`list_domains`,
  `get_domain_status`, `list_dns_records`, `create_dns_record`,
  `delete_dns_record`). Existing CLI commands for those flows now route
  through the same shared connector operation service instead of constructing
  connectors directly.)*
- Add Forge and SSH read/write/exec operations on the same contract. *(Started:
  Forge and SSH now expose public connector definitions and are registered as
  built-in discovery metadata. `ExternalConnectorService` now supports
  Cloudflare write operations (`create_dns_record`, `delete_dns_record`),
  Namecheap DNS write operations (`create_dns_record`, `delete_dns_record`),
  Forge read/write/exec operations (`list_servers`, `get_server`,
  `list_sites`, `get_deployment_script`, `update_deployment_script`,
  `deploy_site`, `exec_site_command`), and SSH operations (`status`, `exec`,
  `stop`). Existing CLI commands for Cloudflare, Namecheap, Forge, and SSH now
  route through the shared connector service for those operations. Standalone
  MCP now also exposes the new Cloudflare delete, Namecheap create/delete, and
  Forge deploy/exec tool surfaces. Those external MCP tools now route through
  the daemon-owned connector client instead of constructing local connector
  instances, and SSH MCP now resolves resource config locally but executes the
  actual SSH/status operations through the same daemon connector boundary.)*
- Enforce destructive-operation acknowledgment across CLI, API, MCP, and
  plugin execution. *(Started: `ExternalConnectorService` and pluginhost
  execution now reject destructive operations unless `acknowledged=true` is
  provided. Cloudflare/Namecheap destructive CLI flows expose `--ack`, Forge
  deploy/exec and SSH exec/stop expose `--ack`, managed/local plugin exec
  commands expose `--ack`, and the MCP tool schemas for destructive external
  operations now accept an `acknowledged` boolean that flows through the same
  daemon-owned execution boundary.)*
- Add agent-facing examples and dry-run output for destructive operations.
  *(Started: destructive Cloudflare, Namecheap, Forge, and SSH connector
  definitions now carry operation examples and `supports_dry` metadata.
  `ExternalConnectorService` now returns structured dry-run previews for those
  destructive built-in operations without contacting provider backends, so
  agents can inspect intended targets, inputs, and warnings before
  acknowledging a change. The corresponding CLI commands now expose
  `--dry-run`, and MCP tool schemas now accept `dry_run` for those
  destructive operations.)*
- Make connector safety metadata directly inspectable by operators and agents.
  *(Started: `cerberus connectors describe <id>` now returns full discovery
  metadata, including operation examples, destructive flags, and dry-run
  support. MCP now exposes `cerberus_connector_describe` on the same daemon
  discovery path.)*

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

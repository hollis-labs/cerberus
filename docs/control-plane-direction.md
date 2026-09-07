# Cerberus Control Plane Direction

**Status:** Directional architecture draft

**Date:** 2026-08-22
**Scope:** Desired product boundaries and architecture; not an implementation plan

## Purpose

This document describes the direction for Cerberus as the Hollis Labs resource
control and observability plane. It records the intended responsibilities,
ownership boundaries, extension model, local-service posture, credential
boundary, and Coder workspace composition model.

It deliberately does not define phases, estimates, task breakdowns, migration
steps, or compatibility strategy. Those belong to a later architecture and
planning session. Existing ADRs remain historical and implementation truth
until explicitly superseded; this document is the direction against which that
future work should evaluate them.

## Portfolio axiom

> Hollis tools own execution and operational state, but not the business
> definitions or business data they operate on.

For Cerberus, this means:

- Projects and publishers own resource definitions, templates, build logic,
  deployment intent, and produced business artifacts.
- External providers own provider-native resource state.
- Credential authorities own secret material and secret lifecycle.
- Operating-system supervisors own durable process supervision.
- Cerberus owns validation, resolved bindings, materializations, leases,
  observations, drift, controlled actions, and operational provenance.

Authority, custody, control, and provenance are separate concerns. Cerberus may
temporarily hold or materialize data without becoming its authoritative owner.
A cached or installed copy is derived state and must never silently become the
source of truth.

## Product definition

Cerberus is a local-first resource control and observability plane. It:

1. Discovers externally owned definitions and registered capabilities.
2. Resolves definitions into concrete provider and driver bindings.
3. Validates desired state and policy before execution.
4. Observes provider-native state and normalizes only genuinely common facts.
5. Computes meaningful diffs between desired and observed state.
6. Materializes or converges resources through registered drivers.
7. Exposes controlled operations through one application contract and several
   transport surfaces.
8. Records provenance, diagnostics, decisions, and operational history.

Cerberus is not the canonical authoring environment for infrastructure logic,
an operating-system process supervisor, a secret manager, an agent runtime, or
a replacement for provider-native tools.

## Architecture sketch

```text
Project repositories / operator configuration / external catalogs
             authoritative definitions and references
                              |
                              v
                  +-----------------------+
Nanite ---------->|                       |
Tether ---------->|       Cerberus        |<---------- Operator / GUI / CLI
Torque ---------->|                       |
                  | registry + validation |
                  | observation + diff    |
                  | policy + control      |
                  | materialization       |
                  | provenance            |
                  +-----------+-----------+
                              |
              registered provider/driver binding
                              |
          +-------------------+--------------------+
          |                   |                    |
          v                   v                    v
   official Go SDK      official CLI       plugin/custom driver
          |                   |                    |
          +-------------------+--------------------+
                              |
          +-------------------+-----------------------------+
          |                   |               |             |
          v                   v               v             v
 OS supervisors        cloud providers    SaaS APIs       Coder
 launchd/systemd/SCM   VPS/DNS/email      GitHub/etc.     workspaces

Credential authority -------- opaque references / scoped grants --------^
```

The caller asks Cerberus for a resource outcome. Cerberus chooses or validates
the provider binding, drives the provider control plane, and returns identities,
observations, and access grants. Interactive or high-volume data paths should
normally connect directly to the underlying substrate after authorization;
Cerberus does not proxy them merely to remain in the middle.

## Responsibility boundaries

| Concern | Authoritative owner | Cerberus responsibility |
|---|---|---|
| Resource definition | Project, operator, or publishing system | Resolve, validate, cache, compare |
| Provider resource | Provider control plane | Observe, request changes, record binding |
| Template content | Template publisher | Validate, materialize, record digest and provenance |
| Build and deployment logic | Project | Execute declared logic and record results |
| Release artifact | Project or artifact authority | Verify and materialize an installed copy |
| Durable service process | Native OS supervisor | Register, control, inspect, and diagnose |
| Development process | Calling workflow | Optionally host as a bounded runner session |
| Credential material | Vault, keychain, provider CLI, or credential broker | Carry references and arrange scoped delivery |
| Workspace intent | Nanite, Tether, Torque, or operator | Provision, lease, observe, and retire infrastructure |
| Workspace infrastructure | Workspace provider such as Coder | Control through provider driver and project status |
| Operational history | Cerberus | Persist observations, diffs, actions, and provenance |

Cerberus may be the operational source of truth for a lease, materialization,
or reconciliation record without becoming the source of truth for the
definition or data that produced it.

## Core domain concepts

The current `Connector` concept conflates implementation, configured provider,
resource semantics, and lifecycle. The desired model separates them.

### Resource kind definition

A `ResourceKindDefinition` describes stable Cerberus vocabulary:

```text
identity and version
desired-state schema
observed-state schema
common conditions
supported operations and actions
readiness semantics
destructive-action annotations
```

Examples include a local service, VPS instance, DNS record, domain
registration, container, workspace, or workspace lease. Kind definitions
describe semantics; they do not contain provider credentials or implementation
code.

### Driver definition

A `DriverDefinition` describes an implementation that can operate one or more
resource kinds:

```text
identity and version
supported resource kinds
implementation transport
provider configuration schema
credential requirements
capabilities and optional actions
compatibility constraints
health and conformance metadata
```

The implementation may be an in-process Go adapter, an official CLI adapter,
or a subprocess plugin.

### Provider registration

A `ProviderRegistration` is an operator-configured instance of a driver:

```text
registration identity
driver reference
endpoint, account, region, or local host binding
credential references
operator policy
enabled capabilities
```

This distinction permits multiple registrations of one driver, such as
`cloudflare-personal`, `cloudflare-hollis`, or multiple Coder deployments.
Resource definitions refer to a provider registration, not directly to an
implementation package.

### Resource

A `Resource` is a generic envelope around a kind-specific desired state:

```text
api version and kind
stable Cerberus identity
project/owner reference
provider selection or requirements
kind-specific desired specification
lifecycle and reconciliation policy
dependencies and provenance references
```

The envelope provides identity, policy, and composition. The kind-specific
specification remains typed within its driver boundary. A universal
`map[string]any` should not become the internal semantic model.

### Observation and conditions

Observed state combines:

- Kind-specific provider state.
- Common conditions such as `Resolved`, `Provisioned`, `Reachable`, `Ready`,
  `Healthy`, `Drifted`, and `PolicyCompliant` where meaningful.
- Raw provider status retained for diagnostics.
- Observation time, source, provider generation, and evidence.

There is no universal `running/stopped` lifecycle for all resources. A GitHub
repository, DNS record, local service, and Coder workspace retain their own
state vocabulary. Cerberus normalizes conditions only where the meaning is
actually shared.

### Materialization

A materialization records how externally owned input became provider-native
state:

```text
source authority and locator
requested revision
resolved immutable revision
content digest
driver and provider registration
provider-native identity
validation result
materialization time and outcome
```

Cached definitions, rendered service units, installed binaries, provider-side
templates, and generated configuration are materializations. They are derived,
replaceable, and provenance-linked to their authoritative sources.

## Resource operations

The common controller vocabulary should express control-plane semantics:

```text
Validate
Observe
Diff
Apply
Delete
```

Kinds and drivers may advertise additional actions such as:

```text
start, stop, restart, reload, shell, logs, rotate, issue_access
```

Unsupported actions are absent from capability discovery. A DNS driver should
not implement meaningless `Start` and `Stop` methods, and a repository should
not be reported as `Running` merely to fit a universal state enum.

Every mutating operation should carry:

- Destructive and reversibility metadata.
- Dry-run or preview support when the provider can support it.
- Required authority and acknowledgement policy.
- Idempotency expectations.
- Structured result, diagnostics, and provenance.

Provider-specific capabilities remain discoverable. Cerberus should not flatten
richer providers to a lowest-common-denominator abstraction.

## Reconciliation posture

Continuous control is a policy dimension, not an assumption attached to every
resource. A resource may choose one of three postures:

```text
Observe
  Detect, cache, and report state or drift. Never mutate automatically.

Explicit
  Compute and present changes, but mutate only after a command or approval.

Converge
  Restore declared state automatically within the resource's granted policy.
```

The daemon may continuously observe every configured resource without having
blanket permission to mutate it. Local durable services may reasonably use
`Converge`; DNS and domain changes may default to `Explicit`; Coder workspace
lifecycle may be driven by an active lease.

This policy makes Cerberus a real control plane while preserving bounded
autonomy and explicit authority.

## Local service direction

### Durable services

Cerberus should register durable services with the native supervisor rather
than act as their supervisor:

```text
Project-owned service definition
             |
             v
Cerberus validate + render + register
             |
             v
launchd | systemd --user | Windows SCM
             |
             v
      supervised process
```

The OS supervisor is authoritative for process state, restart behavior, and
service lifecycle. Cerberus:

- Materializes the supervisor-native registration.
- Requests lifecycle actions through official supervisor interfaces.
- Reads authoritative runtime status from the supervisor.
- Normalizes status and diagnostics for its operator surfaces.
- Detects drift between the registered service and its desired definition.
- Retains the rendered unit and digest as non-authoritative materialization
  evidence.

Startup, restart, demand activation, and persistence are caller-owned service
policy expressed through the resource definition. They should not be hardcoded
by a supervisor adapter.

Conceptually, the desired resource is a `Service`; an operating-system process
is an observed runtime instance of that service. A PID is evidence, not durable
resource identity.

### Development sessions

`dev_session` remains a valid runner capability for local iteration. In this
mode Cerberus really does host and supervise a bounded child process. It should
remain distinct from durable service registration even if both consume a
shared executable specification.

### The Cerberus daemon

The Cerberus daemon exists for long-lived control-plane responsibilities:

- API, MCP, and web availability.
- Configuration and registration observation.
- Provider polling or event ingestion.
- Drift detection and reconciliation where authorized.
- Durable operational history and event emission.

Managed services must not depend on the Cerberus daemon remaining alive. The
native supervisor continues running them independently.

Cerberus installation and self-upgrade should execute through a standalone
installer/CLI control path. A live daemon should not be the sole authority
responsible for replacing its own executable or service registration.

## Build, release, and run

Cerberus provides build and deployment execution without owning project build
logic.

```text
Build
  Execute the project-owned build contract and produce an immutable artifact.

Release
  Bind an artifact digest to environment configuration and external references.

Run
  Materialize and execute that release without rebuilding or silently changing it.
```

The project owns commands, source, inputs, and artifacts. Cerberus validates
the declared contract, executes it, verifies outputs, records provenance, and
materializes the selected artifact. Its artifact store is custody and cache,
not the canonical artifact authority.

Pipeline definitions follow the same rule: the project owns workflow meaning
and step content; Cerberus may own DAG validation, execution mechanics,
cancellation, policy enforcement, and run history.

## Credentials and secrets

Cerberus should not store, create, rotate, update, or delete secret material.
The desired configuration contains opaque credential references only.

```text
CredentialRequirement
  purpose
  audience
  required scopes
  accepted delivery mechanisms

CredentialBinding
  external reference
  selected delivery mechanism
  lifetime
  provenance without material
```

Preferred delivery modes are:

1. **Delegated authentication.** Invoke an authenticated official CLI; the CLI
   and its credential authority handle the secret.
2. **Process-scoped injection.** Ask a credential tool to launch the build,
   deploy, or service process with temporary environment or file material.
3. **Short-lived access grant.** Obtain an audience- and scope-limited token or
   connection grant.
4. **Ephemeral SDK resolution.** Resolve plaintext into memory only for the
   duration of one provider request when delegated mechanisms cannot work.

Secret material must never enter resource definitions, diffs, logs, caches,
events, provenance records, rendered diagnostic output, or the federation
directory. The control plane may validate that a reference is syntactically
valid or available without claiming ownership of its target.

Credential management UX, if present, must be framed as an adapter acting on
an external authority rather than a Cerberus secret store.

## Driver and plugin direction

Cerberus should use existing official tools before implementing provider
protocols itself. Driver selection is based on operational fit rather than a
single mandatory order:

- Prefer an official Go SDK when it is mature, typed, stable, and suitable for
  a long-lived process.
- Prefer an official CLI when it already owns authentication, environment
  discovery, compatibility, and complex workflows.
- Prefer a provider's stable HTTP/OpenAPI surface when official clients are
  unavailable or insufficient.
- Build a custom client only when no appropriate supported tool exists.

CLI adapters require structured machine-readable output, version/capability
detection, bounded execution, argument and environment redaction, and
well-defined failure mapping. SDK adapters isolate provider types behind the
driver boundary and preserve provider-native diagnostics.

Drivers may be delivered as:

1. Built-in Go packages.
2. Registered external executables or official CLIs.
3. Subprocess plugins using the Hollis Labs plugin SDK.

The shared plugin SDK should provide lifecycle, transport, manifest,
configuration, trust, capability grants, packaging, and registration
mechanics. Cerberus adds a host-specific resource-driver contract rather than
creating a parallel general plugin platform.

The Nanite plugin design supplies useful rules:

- Plugins declare; the host validates and grants.
- Preflight validation occurs before process startup.
- Registration is atomic and collision-safe.
- Destructive capabilities require explicit policy.
- Host and plugin compatibility are versioned.
- Real subprocess conformance tests validate the wire contract.

## Workspace direction

Coder is a workspace provider beneath Cerberus, not an AI control plane.
Cerberus should not depend on Coder Agents, Coder's AI Gateway, or direct
general-purpose Coder MCP access.

```text
Nanite -----+
Tether -----+---- acquire/release ----> Cerberus ----> Coder
Torque -----+                             |
                                             Workspace + Lease
                                                     |
Caller ---------------- direct execution/access -----+
```

Each calling application remains independently useful and retains its own
agent, session, job, or workflow semantics. Cerberus receives an opaque caller
and subject reference; it does not adopt those domain models.

### Workspace resource

A `Workspace` is first-class rather than another untyped generic resource. It
contains provider and template bindings, desired power state, declared sources,
capability requirements, environment/network policy, placement, persistence,
and provenance.

Desired infrastructure state is intentionally small:

```text
Running | Stopped | Absent
```

Observed phases are richer:

```text
Pending
Provisioning
Provisioned
Initializing
Ready
Stopping
Stopped
Deleting
Failed
Unknown
```

`Provisioned` and `Ready` are not synonyms. `Ready` is derived from conditions
such as provider provisioning, execution reachability, source initialization,
environment initialization, policy application, and health verification.
`Active` and `Completed` describe caller usage, not workspace infrastructure.

### Workspace lease

A `WorkspaceLease` is a caller's claim over a workspace. It records the
workspace, caller, opaque subject reference, exclusivity, access needs,
expiration/renewal policy, and release disposition.

```text
Pending | Granted | Released | Expired | Revoked | Failed
```

Exclusive leases are the default for agents and jobs. Release disposition may
destroy, stop, or retain the workspace. Human attachment is an access grant
under a lease, not a transfer of ownership.

Task, session, job, workspace, and lease remain separate identities. One work
unit per isolated workspace may be a default policy without becoming a domain
invariant.

### Workspace data and templates

Template publishers own template content and organization. Cerberus resolves,
validates, caches, compares, and materializes provider-native representations.
Coder stores and executes the Coder representation. The workspace records the
immutable resolved template revision and digest used to create it.

Cerberus does not prescribe whether a template is generic, project-specific,
or task-specific. It cares about the external contract: immutable identity,
declared inputs, capabilities, readiness evidence, ownership, and policy.

A workspace may declare zero or more source checkouts. Cerberus records the
requested ref and immutable revision actually materialized. The provider or
initializer establishes initial source state; the caller owns subsequent Git
operations and resulting work.

### Agent execution placement

Nanite owns its agents and launches them through its Agent Host. Tether and
Torque retain their own distinct execution use cases. Cerberus supplies the
workspace; it does not provide a universal agent-session abstraction.

Agent adapters may support:

```text
remote_tools
  The agent loop remains outside the workspace and mediated filesystem/process
  operations execute inside it.

workspace_local
  The agent process itself executes inside the workspace.
```

The desired platform supports both. Remote tools are the baseline composition
because they keep LLM credentials and agent state outside workspace compute.
Workspace-local execution remains available for self-executing CLI agents or
adapters whose internal tools cannot be mediated remotely.

## Resource classes in scope

The Cerberus boundary naturally includes:

- Local durable service registrations and development process sessions.
- VPS instances, images, firewalls, volumes, and provider networking.
- Domains, DNS zones and records, certificates, and provider routing.
- Infrastructure-level email configuration such as domains, verification,
  MX records, routes, and mailboxes where supported.
- Containers and container runtimes.
- Repositories and release-system observations where useful to deployment.
- Build, release, deployment, and health execution.
- Coder and future workspace providers.

Sending business email, owning repository content, interpreting task
completion, defining application workflows, and operating agent sessions are
outside Cerberus even when Cerberus materializes infrastructure for them.

## Current-model tensions to revisit

The current implementation contains valuable prior art but several concepts
need architectural review against this direction:

- `Connector` combines driver, provider registration, and universal lifecycle.
- One connector instance per ID prevents clean multiple-account registration.
- Universal `Create/Start/Stop/Destroy/Status` semantics do not fit all kinds.
- Universal process-shaped resource states erase provider meaning.
- `Resource.Config map[string]any` is useful at a serialization boundary but is
  too weak as the internal semantic model.
- The secret provider exposes storage mutation and connectors eagerly resolve
  plaintext credentials during composition.
- Local `process` conflates desired service identity with observed process
  instances.
- Launchd policy currently embeds choices such as persistent keep-alive that
  should come from the desired service policy.
- Self-deployment through the running daemon creates a circular lifecycle.
- Provider-specific CLI, API, MCP, and GUI handlers can drift unless generated
  or adapted from one capability description.

These are architectural inputs, not a task list.

## Questions for the next architecture session

1. Where may authoritative resource definitions live, and what reference and
   immutable-resolution contract applies to each authority?
2. What is the exact generic resource envelope and how are kind-specific specs
   and statuses registered without falling back to untyped internal maps?
3. What is the provider-registration ownership and override model across
   system, user, project, and per-call configuration?
4. Which operations are universal controller operations and which remain
   discoverable kind-specific actions?
5. What are the default reconciliation postures by resource class, and what
   authority is required to move from `Observe` to `Explicit` or `Converge`?
6. What credential-reference and delivery contract can serve local services,
   provider SDK calls, official CLIs, builds, deployments, and workspaces?
7. Which driver/plugin declarations belong in the shared plugin SDK and which
   remain Cerberus-specific resource semantics?
8. How should provider-side events, polling observations, caches, diffs, and
   provenance be retained without turning derived state into authority?
9. How should local `Service`, `DevSession`, observed `Process`, and installed
   `Release` identities relate?
10. What direct data-plane connection grants should Cerberus issue for Coder
    and other providers without proxying long-lived execution traffic?

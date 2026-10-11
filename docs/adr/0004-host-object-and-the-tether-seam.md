# ADR 0004: Hosts Are a Cerberus Object, and Cerberus Does Not Decide How They Are Used

## Status

Accepted, 2026-10-11. The seam (Cerberus owns hosts, Tether consumes them) is
the operator's explicit direction. The shape of the object and the storage
choice are accepted as the basis for implementation; the questions under "Open
questions", including the on-host agent, stay open and are tracked as tasks.

Workstream: Torque epic **Fleet & Host Management** (`EP-20261011-0001`), task
`CW-20261011-0054`. Settled decision:
`project/cerberus/memory/decisions/cerberus_owns_hosts_tether_consumes`.

## Date

2026-10-11

## Context

An operator's estate is more than the services Cerberus supervises. It is the
machines under them: a few always-on boxes at home, laptops, soon dedicated
cloud boxes, later disposable workers. Today a machine is only a `server`/`ssh`
resource, a named handle with nothing recorded about it. Nothing answers "what
machines do I have, what are they, how loaded are they, and what is set up on
them", and nothing lets an agent or an app ask.

Two pressures arrive together:

- **Capacity.** Agents and apps want to know what compute exists and what is
  free, so they can decide where to launch work.
- **Setup and care.** New machines need the same onboarding every time (network,
  hardening, tailnet, ingress, Cerberus itself), and existing machines need
  monitoring and backups.

`docs/plans/compute-hosting.md` covers the cloud half (a hub, later disposable
workers, a broker) but treats the operator's own machines as a footnote.
Tether's ADR 0064 meanwhile gave Tether its own SSH enrollment and capability
report for workers, and its Fabric 3 epic frames host providers as Tether's.
That puts hardware inventory and machine setup inside an app whose identity is
orchestration.

## Decision

### The seam

**Cerberus manages hardware, setup, infrastructure and ongoing management of
hosts, and owns the host data. Cerberus does not care how a host is used.**

**Tether cares how hosts are used.** It consumes compute through provider
adapters over Cerberus's API and MCP, keeps its own projection, and decides
which resources, compute and services it sets up, configures and exposes. It
does not own compute data and does not install or provision machines.

| Concern | Owner |
|---|---|
| A host exists, its identity, specs, probed capacity, state, cost, lifecycle | Cerberus |
| Onboarding, hardening, tailnet, ingress, monitoring, backups of a host | Cerberus |
| Which host a given agent or workload goes on; placement policy | Tether (or whatever consumes) |
| Agent identity, enrollment, sessions, teams, mailbox | Tether |
| Whether a Tether environment is installed on a host | Tether, recorded by Cerberus only as a pointer |

Consequences of the seam that bind the design:

- Cerberus reports facts (declared and probed). It never exposes a placement
  verb, a "best host" answer or a reservation. A query by constraint returns the
  hosts that satisfy it, unranked by anything but a stable order.
- Cerberus does not branch on a host's purpose. A label such as `agents` or
  `services` is data the consumer sets and filters on. No Cerberus behaviour
  differs by it.
- Where onboarding ends and Tether begins is one hand-off stage: Cerberus can
  invoke `tether init` / `tether service install` / pairing as the last,
  optional stage and records the resulting Tether environment id. Cerberus does
  not reimplement them.

### The Host object

A host is a named admin-lane target with a record Cerberus keeps. It is **not**
a supervised resource (AGENTS.md "Two lanes": remote hosts belong in the admin
lane, and the supervision lane stays local).

| Group | Fields |
|---|---|
| Identity | `name` (stable, unique), `hostname`, `tailnet_name`, `tailnet_ip`, `lan_ip`, `role`, `provider`, `labels` |
| Reach | an ssh target reference (an existing `server`/`ssh` resource or inline), transport preference |
| Declared | `os`, `arch`, nominal `cpu`, `memory`, `disk`, `gpu`: what the operator says the machine is |
| Probed | the same facts as measured, plus load, free memory, free disk, tool presence (docker, tmux, tailscale, caddy), `probed_at` |
| State | `online` / `offline` / `unknown`, `onboarding_stage`, `last_seen` |
| Lifecycle | `created_at`, `owner`, `ttl` (cloud and disposable hosts), `cost` (monthly estimate, cloud hosts) |
| Pointers | `tether_environment_id` (opaque, set by the hand-off), backup policy references |

Declared and probed values are stored side by side and never merged: a drift
between them is information (a disk that shrank, a machine that is not what it
was declared to be).

`role` is descriptive (`hub`, `operator`, `worker`, `builder`, `service`) and
follows `compute-hosting.md`; Cerberus uses it only for display and filtering.
`provider` is `homelab` or a provider plugin id (`digitalocean`, `hetzner`);
it decides how the host is created and destroyed and nothing else.

Cloud hosts and homelab hosts are the same object. Provisioning a droplet
creates a Host, destroying it removes the Host, and the broker work in
`compute-hosting.md` becomes a consumer of this object.

### Storage

- **Declared data** is operator-owned config, `~/.cerberus/hosts.yaml`, outside
  every repo. Real hostnames, addresses and specs never enter this repo
  (AGENTS.md: operator-specific notes belong in the operator's own notes).
- **Derived data** (probed values, state, `last_seen`, onboarding progress) is
  kept by the daemon in its own state, can be discarded and rebuilt by probing,
  and reports its age and a `stale` flag.
- A host may also be declared by a descriptor in a project, like resources, and
  registered the same way, so a repo can ship the host set it needs without
  naming the operator's machines.

### Probing

A probe is a read-only, per-call admin-lane operation over ssh, using the
`sysmon` plugin for the measurements and resolving every tool per call (the
daemon's `PATH` is not the shell's). An unreachable host is a state, not an
error. Probe results are DTOs (ADR 0003) with an allow-list of fields.

### Surfaces

One runtime service, as everywhere else: CLI (`cerberus host list|get|probe|
capacity`), daemon socket, HTTP API, console and MCP, generated together. The
capacity query takes constraints (minimum free memory, cpu, disk; labels; role;
capability) and returns matching hosts with their facts. MCP results carry what
an agent must act on in the text, not `_meta`.

### Onboarding

A staged, resumable, dry-run-capable pipeline. Stages: preflight, network, base
packages, hardening, tailnet join, ingress (Caddy), Cerberus install, then the
optional hand-offs (the tmux team kit, Tether enrollment). Each stage is
idempotent, previews before it writes, requires `--ack` for writes, and is
audited. Steps only a human can do (CLI logins, a tailnet approval, authorizing
a key) are explicit pending checkpoints, not hidden failures. The stage the
host has reached is recorded on the Host.

Linux and macOS share most stages; Windows is a later profile. The existing
operator bootstrap scripts are the input for the Linux and macOS stages, not
the design.

### The on-host agent: open

Whether to install a Cerberus-owned daemon on managed hosts is a decision with
real alternatives and is **deferred to a spike** (`CW-20261011-0065`):

- **No install.** The controlling Cerberus polls over ssh and `sysmon`. Nothing
  new on the host, nothing to update. Slower to notice change, and activity and
  log streams are awkward.
- **Reuse the host's own Cerberus.** Invariant H4 already puts a Cerberus on
  every machine. It could report to the controlling one. No new binary.
- **A dedicated lightweight agent.** Smallest footprint and a push model, but a
  new binary, a new update path and a new credential to protect.

The first option is enough for F1 and F2. The spike chooses between the second
and third, and must satisfy H2 (tailnet only) and H3 (every client
authenticates).

## Alternatives considered

- **Tether owns the host inventory and installs workers (ADR 0064 as written).**
  Rejected by the operator. Managing hardware does not fit Tether's identity,
  and neither does owning that data. The Tether-side amendment is
  `CW-20261011-0056`.
- **Hosts as supervised resources.** Rejected: the supervision lane is
  deliberately local and process-shaped, and AGENTS.md forbids widening it to
  reach remote systems.
- **Cerberus ranks or reserves hosts for consumers.** Rejected: that is a
  placement policy, which is exactly what the seam keeps out of Cerberus.
- **Inventory in the repo.** Rejected: it would put real hostnames and
  addresses into a public repo.

## Consequences

- A new registry type, a probe, a capacity query and an onboarding pipeline are
  Cerberus core work. Provider-specific and tool-specific parts stay plugins:
  `sysmon`, `tailscale`, `caddy`, `tmux`, `ufw`, `apt`, `fail2ban`, and the
  cloud providers.
- Tether's provider adapter for Cerberus is a thin consumer of the capacity
  surface. It depends on that surface staying fact-only.
- Cloud work (dedicated service boxes, offsite backups) lands on the same object
  instead of a parallel inventory.
- The host record is operator-sensitive. It goes through the redaction scope
  like any other response, and nothing in it is a credential.
- The two Tether ADR/epic amendments and the retirement of the team kit's
  `identities.tsv` are tracked outside this repo.

## Open questions

- Is `~/.cerberus/hosts.yaml` the right single file, or should hosts live in the
  main config registry with the resources they sit under?
- How is a host's identity tied to a Tether environment when the machine is
  re-imaged or renamed (the pointer is opaque, so which side re-establishes it)?
- Does the capacity query need a reservation or lease for consumers that must
  not race each other, and if so does that live in the consumer instead?
- The on-host agent decision above.
- How the probe authenticates on employer-owned machines where only identity-
  based ssh is permitted.

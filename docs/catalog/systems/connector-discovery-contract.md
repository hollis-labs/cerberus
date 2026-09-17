---
id: "CERB-CAP-210"
class: "capability"
name: "Connector discovery contract"
summary: "Declares each connector's operations, input schemas, destructive and dry-run flags as data, so `connectors describe` is authoritative rather than a docstring."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "Ran describe for all nine connectors against the daemon and diffed the flags against source"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "pkg/connector/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "contract"
  - "discovery"
  - "plugin-api"
  - "schema"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "The admin lane reads these flags to decide ack and dry-run"
  - type: "blocks"
    target: "CERB-GAP-278"
    note: "requires_ack exists in the manifest and is dropped on the way to the Definition"
  - type: "relates_to"
    target: "CERB-CAP-304"
    note: "A plugin declares the same shape in plugin.yaml"
---

# Connector discovery contract

> Declares each connector's operations, input schemas, destructive and dry-run flags as data, so `connectors describe` is authoritative rather than a docstring.

Every connector declares itself as data. `Definition` carries the id, the
resource types it serves, a `Capabilities` struct, a config schema naming fields
and secret requirements, and a list of `Operation`s — each with a name, a
description, examples, a JSON input schema, and two flags: `Destructive` and
`SupportsDry`.

This is why `cerberus connectors describe <id>` is authoritative and reading a
`switch` statement is not. Across all nine connectors the declared flags are:
48 operations, 14 destructive, 13 dry-runnable. The one destructive operation
with no dry-run is `docker destroy`. The flags are also the only thing the
enforcement points read, so a mis-declared operation is a mis-enforced one:
`digitalocean create_droplet` and `digitalocean stop` are dry-runnable but not
destructive, so creating a billable droplet and powering a host off both take no
`--ack`; and `forge update_deployment_script` replaces what runs on every future
deploy of a site while being flagged neither.

`Manifest` is the same shape plus one field the public `Definition` does not
have: `RequiresAck`. `ManifestFromDefinition` derives it — `RequiresAck:
op.Destructive` — and `DefinitionFromManifest` drops it again. The round trip is
lossy in exactly one direction, and the direction it loses is the one that faces
callers: nothing in `connectors describe` tells an agent which operations need
acknowledgment. It has to infer it from `destructive`, which happens to be
correct for the built-ins because the manifest derives one from the other, and
is *not* guaranteed for a plugin, whose `plugin.yaml` sets both independently.

The interface half is deliberately split. `Connector` is the lifecycle contract
— `Create`, `Start`, `Stop`, `Destroy`, `Status`, `Capabilities` — and
`Describer` is separate so a connector can adopt discovery metadata
incrementally. In practice all nine implement both. The lifecycle interface is
also a poor fit for the admin lane's richer verbs: `ssh exec`, `docker logs` and
every `list_*` reach the connector through its concrete type after a type
assertion in `execute<X>`, not through the interface. The interface constrains
the supervision-shaped operations; everything else is per-connector surface.

## Owns

- The Connector interface every provider implements: Create, Start, Stop, Destroy, Status, Capabilities
- The Describer interface and Definition: operations, JSON input schemas, config fields, secret requirements
- Per-operation Destructive and SupportsDry flags
- Manifest validation for built-in and plugin connector declarations
- Conversion between the public Definition and the host-policy Manifest

## Does not own

- Enforcing the flags. Destructive and SupportsDry are declarations; the admin lane and the plugin lane act on them
- Connector liveness. That is the registry's Probe
- Response types. ADR 0003 governs those and the contract does not check them
- MCP tool naming for built-ins — only plugin connectors get generated names

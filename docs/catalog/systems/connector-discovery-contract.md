---
id: "CERB-CAP-210"
class: "capability"
name: "Connector discovery contract"
summary: "Declares each connector's operations, input schemas, destructive and dry-run flags as data, so `connectors describe` is authoritative rather than a docstring."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.93
confidence_label: "Operation counts and flags read from connectors describe on a branch build of main after P0; schema drift test read"
last_reviewed: "2026-09-25"
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
    note: "resolved in PR #49: destructive alone now decides acknowledgment on both lanes"
  - type: "relates_to"
    target: "CERB-CAP-304"
    note: "A plugin declares the same shape in plugin.yaml"
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "ssh and docker schemas advertise only what the configured-target rule accepts"
---

# Connector discovery contract

> Declares each connector's operations, input schemas, destructive and dry-run flags as data, so `connectors describe` is authoritative rather than a docstring.

Every connector declares itself as data. `Definition` carries the id, the
resource types it serves, a `Capabilities` struct, a config schema naming fields
and secret requirements, and a list of `Operation`s — each with a name, a
description, examples, a JSON input schema, and two flags: `Destructive` and
`SupportsDry`.

This is why `cerberus connectors describe <id>` is authoritative and reading a
`switch` statement is not. Across the seven built-in connectors, read from a
branch build of main after P0, the declared flags are 40 operations, 16
destructive and 14 dry-runnable. The destructive operations with no dry run are
`docker destroy` and `forge update_deployment_script`, and a dry run of either
is refused as `preview_unsupported`. The flags are also the only thing the
enforcement points read, so a mis-declared operation is a mis-enforced one. At
audit time `digitalocean create_droplet`, `digitalocean stop` and
`forge update_deployment_script` were all writes flagged non-destructive, so
none of them took `--ack`. PR #49 corrected all three (CERB-GAP-279).

**The schemas match enforcement for ssh and docker.** After PR #50 and PR #52,
the socket, web and MCP refused ssh connection fields and docker ad-hoc targets,
but the Definitions still advertised them as operation inputs, so an agent that
built a call from the schema was refused. PR #54 builds each operation's input
schema from the table enforcement uses: `sshconn.OperationFields` for ssh, where
every operation requires `id`, and `CallerKeys()` in
`internal/connector/docker/config_keys.go` for docker, where no target key
appears in any schema. `TestDiscoverySchemasMatchEnforcement` fails when a
schema advertises a key enforcement refuses, and when enforcement accepts a key
no schema advertises. `Config.Fields` still lists `host`, `key_file` and the
rest, because they describe what a resource declares, and the web console now
labels that panel as set on the resource, not per call.

`Manifest` is the same shape plus one field the public `Definition` does not
have: `RequiresAck`. `ManifestFromDefinition` derives it — `RequiresAck:
op.Destructive` — and `DefinitionFromManifest` drops it again. The round trip is
lossy in exactly one direction. At audit time that mattered, because a plugin's
`plugin.yaml` set `destructive` and `requires_ack` independently and the plugin
host demanded acknowledgment only when both were true. Since PR #49 the host
gates on `destructive` alone for plugins as for built-ins, and `requires_ack` is
deprecated metadata, still required on a destructive operation so older hosts
keep gating. So `destructive` in `connectors describe` is now the whole answer to
"does this need acknowledgment" (CERB-GAP-278).

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

## Since PR #60

Discovery now serves the operation contract (CERB-CAP-212):

- **Contract fields:** `effect`, `reversible`, `target`, `preview`, `output`,
  `cost`, `local_fs` and `requires_ack`, alongside the derived `destructive`
  and `supports_dry`.
- **`destructive`** now means `effect == destructive` only. Gate on
  `requires_ack`, which is the whole answer to "does this need
  acknowledgment". The paragraph above that says `destructive` was that answer
  describes the state before PR #60.
- **`input_schema`** is built from the key table's caller-scope inputs, so
  local-only docker keys are never advertised.
- **Schema drift is impossible.** `Finalize` derives the schema from the key
  table, so the two cannot disagree for built-ins, and the conformance suite
  checks the same for plugin manifests.

---
id: "CERB-CAP-200"
class: "capability"
name: "The admin / connector lane"
summary: "Runs one imperative verb against one external system per call, resolving the connector, its credential and its target fresh every time."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "Nine connectors described live from the running daemon; four connectors exercised live"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/external_connector_service.go"
tags:
  - "acknowledgment"
  - "admin-lane"
  - "cerberus"
  - "class:capability"
  - "connector"
  - "dry-run"
  - "mcp"
  - "redaction"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-210"
    note: "The discovery contract this lane reads destructive and dry-run flags from"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "The other lane; deliberately not widened to reach remote systems"
  - type: "relates_to"
    target: "CERB-CAP-301"
    note: "Managed plugin connectors dispatch through this lane but are acknowledged in the plugin lane"
  - type: "blocks"
    target: "CERB-GAP-270"
    note: "The dry-run fallthrough lives in this service's Execute"
  - type: "blocks"
    target: "CERB-GAP-273"
    note: "This service's error formatting is what redaction eats"
---

# The admin / connector lane

> Runs one imperative verb against one external system per call, resolving the connector, its credential and its target fresh every time.

`external_connector_service.go` is where a verb meets a remote system. It is
stateless by construction: `Execute` takes a connector id, an operation name, a
config map, and two booleans — `dry_run` and `acknowledged` — and resolves
everything else per call. There is no session, no cached host and no cached
credential, which is the property that makes one daemon able to administer the
work host and a cloud account in consecutive calls without either becoming a
default the next call inherits.

The shape of `Execute` is worth reading in order, because the order is the
policy:

1. A hard refusal for `namecheap create_dns_record` / `delete_dns_record`,
   deliberately placed **before** credential resolution so the refusal is what
   an operator sees rather than a missing-token error. Verified: `cerberus dns
   create` on a machine with no Namecheap credential returns the refusal and its
   recovery, not `credential_missing`.
2. The dry-run branch. If `dryRunPreview` has a case for this connector and
   operation, it returns a preview and stops.
3. Managed plugin dispatch, if a plugin owns this id.
4. `registry.Resolve`, which runs the connector's factory — so a missing binary
   or an unset token fails here, per call.
5. `requireAcknowledgment`, which refuses a `Destructive` operation that did not
   arrive acknowledged.
6. A `switch` on connector id into `execute<X>`.

Two things about that order matter more than they look. **Acknowledgment is
enforced centrally**, in one function, driven by the `Destructive` flag on the
connector's own declared `Operation` — not re-implemented per connector. That is
the version of this you want. But it is enforced *after* credential resolution,
so on a machine without the token you cannot reach the gate to test it; and it
is enforced *after* the managed-plugin branch has already returned, so a plugin
connector's destructive operation is gated by
`pluginhost.OperationAllowed` instead, on a `requires_ack` field the plugin's
own manifest sets.

And **the dry-run branch falls through when it finds no case.** For a
destructive operation that is safe — it lands on the acknowledgment gate and
refuses, which is the behaviour `docs/plans/connector-work-packages.md` records
as a known trap. For a *non*-destructive operation there is no gate to land on,
so `dry_run: true` is silently ignored and the operation runs for real. The MCP
tool `cerberus_droplet_start` advertises a `dry_run` parameter described as
"Preview only", and DigitalOcean `start` has no preview case.

Dry-run itself is not a connector capability here. Every preview in the product
lives in one ~220-line `dryRunPreview` switch in this file, covering 13
operations across five connectors. A connector declaring `SupportsDry: true`
does not implement anything; it is a claim that this switch has a case for it.
The claim and the case agree today for all 13 — checked one by one against
`cerberus connectors describe` — which is a fact about care, not about
structure. Extracting them is WP-1, and WP-1 was skipped rather than done.

Error classification is the lane's other real product. `credential_missing` is
distinguished from `connector_unavailable` by scanning the error text for
"token", "credential", "secret" or "api key", which is how a plugin missing a
declared secret and a built-in missing a token report the same code. Then
`ExternalConnectorError.Error()` formats `"<connector> <op>: <code>: <cause>"`
and runs `redact.Text` over the whole string — and that is where the lane
currently breaks its own rule. `credential_missing:` parses as a credential
assignment, so the first word of the cause is replaced. Verified live:
`cerberus github status hollis-labs/cerberus` returns
`credential_missing: [REDACTED] connector: no API token...`. The recovery
survives; the connector's own name does not.

## Owns

- Per-call connector resolution through the registry, so a rotated or missing secret is never cached
- Central acknowledgment enforcement for destructive built-in operations
- Every dry-run preview in the product, in one switch
- Classifying failures into connector_unavailable, credential_missing, operation_unsupported, invalid_args and acknowledgment_required
- Running redact.Text over operator-facing error text
- MCP progress and message notifications for long operations

## Does not own

- Supervision. Nothing here restarts, health-probes or watches anything; that is resource_runtime_service.go
- State between calls. There is no session, no cached host, no cached credential
- Credential storage. It asks the secret provider and never writes one
- MCP tool definitions for built-in connectors — those are hand-written in internal/mcp/tools_<x>.go
- Acknowledgment for plugin connector operations, which the plugin lane's OperationAllowed enforces separately
- An audit trail. Nothing records who ran which destructive verb

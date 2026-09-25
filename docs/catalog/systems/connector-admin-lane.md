---
id: "CERB-CAP-200"
class: "capability"
name: "The admin / connector lane"
summary: "Runs one imperative verb against one external system per call, resolving the connector, its credential and its target fresh every time."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.93
confidence_label: "Execute, requireAcknowledgment and the refusals re-read on main after P0 (#48 to #54); operation counts from connectors describe on a branch build"
last_reviewed: "2026-09-25"
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
    note: "The dry-run fallthrough lived in this service's Execute; closed in PR #49"
  - type: "blocks"
    target: "CERB-GAP-273"
    note: "This service's error formatting was what redaction ate; closed in PR #34"
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "SSH and docker targets are resolved from a configured resource at the top of Execute"
  - type: "blocks"
    target: "CERB-GAP-847"
    note: "SSH and docker enforce caller keys in two different places"
  - type: "blocks"
    target: "CERB-GAP-851"
    note: "credential resolution runs before the acknowledgment gate"
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

1. Target resolution for `ssh` and `docker` (PR #50, PR #52). An SSH
   operation's config may carry `id` plus its operation fields and nothing else,
   and the id is resolved against whoever runs the operation: the daemon's live
   config snapshot, or the CLI's own config in-process. A docker operation may
   name `resource: <id>`, resolved the same way. This runs before any preview,
   so a preview names the real target (CERB-DEC-813).
2. *(Retired 2026-09-25.)* The hard refusal for `namecheap create_dns_record` /
   `delete_dns_record` left the host with the built-in. The namecheap plugin
   does not declare either operation, so the contract gate below refuses them
   as undeclared before anything resolves, and the plugin refuses them by name
   as well for a caller that reaches it directly.
3. The dry-run branch. If `dryRunPreview` has a case for this connector and
   operation, it returns a preview and stops. If it has none, the call is
   refused as `preview_unsupported` and nothing runs, unless a loaded managed
   plugin owns the id, in which case the plugin host makes the same decision
   from the manifest's `supports_dry`.
4. Managed plugin dispatch, if a plugin owns this id.
5. `registry.Resolve`, which runs the connector's factory — so a missing binary
   or an unset token fails here, per call.
6. `requireAcknowledgment`, which refuses a `Destructive` operation that did not
   arrive acknowledged, and refuses as `operation_unsupported` any operation the
   connector's definition does not declare, or a connector with no definition.
   It fails closed (PR #49).
7. A `switch` on connector id into `execute<X>`.

Two things about that order matter more than they look. **Acknowledgment is
enforced centrally**, in one function, driven by the `Destructive` flag on the
connector's own declared `Operation` — not re-implemented per connector. That is
the version of this you want. But it is enforced *after* credential resolution,
so on a machine without the token you cannot reach the gate to test it
(CERB-GAP-851). And it is enforced *after* the managed-plugin branch has already
returned, so a plugin connector's destructive operation is gated by
`pluginhost.OperationAllowed` instead. Since PR #49 that gate reads the same
`Destructive` flag and ignores the manifest's `requires_ack`, so a plugin can no
longer opt out of it (CERB-GAP-286).

And **a dry run never executes.** Until PR #49 the dry-run branch fell through
when it found no case. A destructive operation then landed on the
acknowledgment gate, or, if acknowledged too, ran: `docker destroy --dry-run
--ack` destroyed. A non-destructive one had no gate at all, so
`cerberus_droplet_start {dry_run: true}` booted the droplet. Now a missing
preview is a `preview_unsupported` refusal that comes before credential
resolution and the gate, and `TestDryRunNeverExecutes` sweeps every declared
operation against factories that only count (CERB-GAP-270).

Dry-run itself is not a connector capability here. Every built-in preview
lives in one `dryRunPreview` switch in this file, covering all 14 dry-runnable
built-in operations across five connectors. A connector declaring
`SupportsDry: true` does not implement anything; it is a claim that this switch
has a case for it. That claim used to be held true by care. Since PR #49
`TestDryRunNeverExecutes` requires every operation declaring `supports_dry` to
have a preview and every other operation to get `preview_unsupported`, so a
test now holds the claim and the case in step. Extracting them is WP-1, and
WP-1 was skipped rather than done.

Error classification is the lane's other real product. `credential_missing` is
distinguished from `connector_unavailable` by scanning the error text for
"token", "credential", "secret" or "api key", which is how a plugin missing a
declared secret and a built-in missing a token report the same code. Then
`ExternalConnectorError.Error()` formats `"<connector> <op>: <code>: <cause>"`
and runs `redact.Text` over the whole string. That used to break the lane's own
rule: `credential_missing:` parsed as a credential assignment, so the first word
of the cause was replaced (CERB-GAP-273). PR #34 taught the redactor the lane's
error-code vocabulary. PR #49 added `preview_unsupported` to it, with a test
that every code followed by a recovery sentence survives `redact.Text`.

## Owns

- Per-call connector resolution through the registry, so a rotated or missing secret is never cached
- Resolving an SSH or docker target from a configured resource id before anything else runs
- Central acknowledgment enforcement for destructive built-in operations, failing closed on an undeclared operation
- Every built-in dry-run preview, in one switch, and the preview_unsupported refusal for everything else
- Classifying failures into connector_unavailable, credential_missing, operation_unsupported, invalid_args, acknowledgment_required and preview_unsupported
- Running redact.Text over operator-facing error text
- MCP progress and message notifications for long operations

## Does not own

- Supervision. Nothing here restarts, health-probes or watches anything; that is resource_runtime_service.go
- State between calls. There is no session, no cached host, no cached credential
- Credential storage. It asks the secret provider and never writes one
- MCP tool definitions for built-in connectors — those are hand-written in internal/mcp/tools_<x>.go
- Acknowledgment for plugin connector operations, which the plugin lane's OperationAllowed enforces separately, on the same destructive flag
- The audit sink. Execute writes to it, but the log itself — storage, chain, verification — is internal/audit (CERB-CAP-604)

## Since P1 (PRs #57 and #60, and the P1-3 branch)

`Execute`'s order is now fixed and fail-closed:

1. The namecheap per-record refusal.
2. The contract gate: the operation must be declared, and the raw config must
   pass its key table for the caller's surface (CERB-CAP-212, CERB-DEC-817).
3. ssh and docker resource resolution.
4. Dry run.
5. Plugin dispatch.
6. The acknowledgment check.
7. `Resolve`.
8. Execution.

No argument or acknowledgment refusal depends on a credential. Refusals travel
the socket with their code (`connectorErrorWire`), so every surface maps them
through one status table. An error from past the gates that carries no code is
`operation_failed`, 502. The P1-3 branch added JSON type and enum checks to the
key table, swept over every declared operation.

Since P1-4a, `Execute` is wrapped by the audit log (CERB-CAP-604). An intent
record is written before step 1 and an outcome record on every exit. When the
intent cannot be written, a non-read is refused as `audit_unavailable` and none
of the steps run. The service takes its audit sink as a required constructor
argument and is built outside tests only in `internal/app`.

---
id: "CERB-CAP-401"
class: "capability"
name: "Daemon socket API"
summary: "A stdlib HTTP API over a 0600 unix socket that every other surface is a client of, and the only surface where all 48 connector operations are reachable, via one generic operations route."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.93
confidence_label: "Routes read from source; /health, /ping, /connectors queried live against the running daemon"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/socket_server.go"
tags:
  - "cerberus"
  - "surface"
  - "socket"
  - "api"
  - "daemon"
  - "area:surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "the socket column in the surface matrix"
  - type: "implements"
    target: "CERB-TOOL-413"
    note: "the generic connector operations route"
  - type: "implements"
    target: "CERB-TOOL-416"
    note: "NDJSON progress streaming"
  - type: "blocks"
    target: "CERB-GAP-444"
    note: "API version header enforced only when present"
  - type: "blocks"
    target: "CERB-GAP-449"
    note: "ensure-fresh has no route"
  - type: "relates_to"
    target: "CERB-CAP-106"
    note: "the guard lives in cerbapi/daemon_self_guard.go"
---

# Daemon socket API

The daemon serves an HTTP API over a unix socket at `~/.cerberus/cerberus.sock`,
chmod `0600`, stdlib `net/http` on `net.Listen("unix", …)` — no framing, no
gRPC, no hand-rolled JSON-RPC. It is the real API: the CLI, the web console and
both MCP adapters are all clients of it, and it is the only surface where
*every* connector operation is reachable.

Fifteen route patterns:

```
GET  /health                                   GET  /ping
GET  /projects                                 GET  /registry/diagnostics
GET  /resources                                     /resources/{id}/{action}
GET  /pipelines                                     /pipelines/{id}/run
GET  /connectors                               GET  /connectors/live
POST /connectors/{id}/operations/{op}
GET  /plugins/connectors                            /plugins/connectors/{id}/…
POST /plugins/connectors/health
POST /plugins/connectors/operations/{op}
```

`/resources/{id}/{action}` covers `doctor`, `inspect`, `status`, `logs` (GET)
and `deploy`, `apply`, `reload`, `stop`, `sync`, `remove` (POST). There is no
`ensure-fresh` route: `cerbapi.EnsureFresh` is a client-side composite over
status plus deploy/apply/sync, so the verb exists on the CLI and in MCP but not
in the API those two call.

Two things make this the most complete surface:

**A generic connector route.** `POST /connectors/{id}/operations/{op}` takes an
`ExternalConnectorOperationArgs` body — arbitrary `config`, `dry_run`,
`acknowledged` — and hands it to `ExecuteConnectorOperation`, which resolves
built-in and managed-plugin connectors alike. All 48 operations across the 9
registered connectors are reachable without a line of per-operation code.
`/plugins/connectors/{id}/operations/{op}` is the plugin-scoped equivalent.

**NDJSON progress streaming.** A request carrying `X-Cerberus-Progress: 1` gets
`application/x-ndjson` back: a stream of `{"type":"notification"|"result"|
"error"}` envelopes. `external_connector_service.go`'s `gmcp.NotifyMessage` /
`NotifyProgress` calls become notification envelopes; `SocketClient` re-emits
them with `gmcp.Notify(ctx, …)`. `doJSONStream` is used for deploy, apply, sync,
pipeline run, connector operations and the whole plugin lifecycle.

Authorisation is filesystem permissions and nothing else — mode `0600`, same
uid as the daemon, which is the correct model for a local socket. API versioning
is via `X-Cerberus-Api`, and the wrapper rejects a *mismatched* header but
accepts a *missing* one, so version negotiation is opt-in and an unversioned
client is served silently.

The daemon deliberately refuses one class of call: `daemon_self_guard.go` blocks
resource mutations targeting the daemon's own resource, because deploying the
daemon through its own socket kills the call on EOF and can leave the launchd
job booted out.


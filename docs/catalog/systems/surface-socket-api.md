---
id: "CERB-CAP-401"
class: "capability"
name: "Daemon socket API"
summary: "A stdlib HTTP API over a 0600 unix socket that every other surface is a client of, and the only surface where all 48 connector operations are reachable, via one generic operations route."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.93
confidence_label: "routes and refusals re-read on main after P0 (#48 to #54)"
last_reviewed: "2026-09-25"
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
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "ssh and docker configs are limited to a resource id on the socket"
  - type: "blocks"
    target: "CERB-GAP-849"
    note: "install by path retired in P1-5 (410)"
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
POST /plugins/connectors/health               (410 since PR #50)
POST /plugins/connectors/operations/{op}      (410 since PR #50)
```

`/resources/{id}/{action}` covers `doctor`, `inspect`, `status`, `logs` (GET)
and `deploy`, `apply`, `reload`, `stop`, `sync`, `remove` (POST). There is no
`ensure-fresh` route: `cerbapi.EnsureFresh` is a client-side composite over
status plus deploy/apply/sync, so the verb exists on the CLI and in MCP but not
in the API those two call.

Two things make this the most complete surface:

**A generic connector route.** `POST /connectors/{id}/operations/{op}` takes an
`ExternalConnectorOperationArgs` body (`config`, `dry_run`, `acknowledged`) and
hands it to `ExecuteConnectorOperation`, which resolves built-in and
managed-plugin connectors alike. Every declared operation is reachable without a
line of per-operation code. Since P0, `config` is not arbitrary for two
connectors. An ssh operation takes a resource `id` plus its operation fields,
and a docker operation takes `resource`, the container keys and `id`/`lines`.
The socket refuses everything else by name, and the daemon resolves the id
against its live config (CERB-DEC-813). A dry run returns a preview or
`preview_unsupported` and never executes.
`/plugins/connectors/{id}/operations/{op}` is the managed-plugin equivalent, and
refuses a `plugin_dir` field.

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

Same-uid is also why P0 took one capability away from the socket rather than
guarding it. Before PR #50, `/plugins/connectors/health` and
`/plugins/connectors/operations/` installed, loaded and ran the entrypoint of
whatever `plugin_dir` a caller named. Both answer 410 now, and the daemon no
longer builds a `PluginConnectorService`, so no surface can make the daemon run
a directory it was not told to install. `POST /plugins/connectors/install` took a path,
for the CLI, until P1-5 retired it (CERB-GAP-849).

The daemon deliberately refuses one class of call: `daemon_self_guard.go` blocks
resource mutations targeting the daemon's own resource, because deploying the
daemon through its own socket kills the call on EOF and can leave the launchd
job booted out.

## Since P1-5

No socket route takes a directory. `POST /plugins/connectors/install` answers
410 with `PluginInstallRetired`, which names the terminal command, because an
install is a review confirmed on the operator's TTY and a socket request cannot
carry that. `POST /plugins/connectors/{id}/reload` is new. It takes an id, makes
the daemon re-read that entry from the reviewed state file, and refuses a bundle
that does not match its accepted digest as `plugin_changed` (409) without
stopping the running plugin. Load and reload now answer with the coded error's
status and keep the code on the wire.

## Since P2-1

Authorisation is no longer the file mode alone. Each connection's peer
credentials are read from the kernel when it is accepted (`ConnContext` in
`socket_server.go`). On darwin that is `LOCAL_PEERCRED` and `LOCAL_PEERPID`
(`peercred_darwin.go`), on linux `SO_PEERCRED` (`peercred_linux.go`), and any
other platform refuses (`peercred_other.go`). `checkPeer` refuses a request
whose peer runs as another uid, or whose credentials cannot be read, as
`principal_refused` (403), before any handler runs. That proves the local user,
not human versus agent.

A caller says who it is in `X-Cerberus-Principal`: kind, via, client, session
and on_behalf_of, never a uid. `BeginHTTPRequest` reads it on the socket only,
records it as self-reported and adds the peer's uid, marked verified. A missing
claim is an agent via `unknown`, and a caller claiming `automation` is read as
an agent, because only Cerberus itself is automation. `GET /whoami` answers
with the principal the daemon gives the request, which is what
`cerberus whoami` shows (CERB-TOOL-419). The principal is a label for default
policy and never approval (CERB-GAP-859).

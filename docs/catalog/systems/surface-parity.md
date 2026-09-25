---
id: "CERB-CAP-404"
class: "capability"
name: "Surface parity"
summary: "The five-surface matrix: 48 connector operations reach the socket API, console HTTP API and console UI, 44 reach the CLI and 33 reach MCP, because only the API and console resolve operations generically."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "Operation lists from connectors describe at audit time; plugin rows and the generic route re-read on main after P0 (#48 to #54)"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "docs/catalog/systems/surface-parity.md"
tags:
  - "cerberus"
  - "surface"
  - "parity"
  - "matrix"
  - "agent"
  - "area:surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-400"
    note: "the CLI column"
  - type: "relates_to"
    target: "CERB-CAP-401"
    note: "the socket column"
  - type: "relates_to"
    target: "CERB-CAP-402"
    note: "the MCP column"
  - type: "relates_to"
    target: "CERB-CAP-403"
    note: "the HTTP and console columns"
  - type: "blocks"
    target: "CERB-GAP-430"
    note: "the root cause: MCP tools are hand-written"
  - type: "blocks"
    target: "CERB-GAP-438"
    note: "verb-level CLI/MCP asymmetries"
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "the operations being counted are its leaves"
  - type: "relates_to"
    target: "CERB-CAP-303"
    note: "plugin operations are the largest MCP gap"
---

# Surface parity

Cerberus's stated premise is that every capability added to a connector becomes
a CLI command, an API operation and an MCP tool at the same time, which is what
makes it usable by agents as well as by people. Tested against the running
system, that premise holds for the API and the console, and does not hold for
the CLI or for MCP.

The reason is architectural, not accidental. The socket API has **one generic
route** for connector operations, `POST /connectors/{id}/operations/{op}`,
which takes a config body (limited to a resource id and operation fields for
ssh and docker since P0) and resolves built-in and plugin connectors alike. The console inherits it and drives it generically: the
Connectors page iterates `connector.operations` from the discovery metadata and
renders a Run button, a config input (a resource picker for ssh and docker) and dry-run/ack toggles for each. Both
are mechanical, so a new connector operation reaches them for free.

The CLI and MCP are the opposite. Every operation they expose is a hand-written
subcommand or a hand-written `NewCerberusXxxTool` constructor, and both lists
are hand-maintained — MCP's twice, once in `cmd_mcp.go` and again inline in
`cmd_daemon.go`. Neither has a generic escape hatch for built-in connectors.
The result:

- **48** connector operations exist across 9 connectors.
- **48** are reachable on the socket API, the console HTTP API and the console UI.
- **44** are reachable from the CLI.
- **33** are reachable from MCP.

The 15 operations MCP cannot reach include everything both loaded plugins
offer. Azure's six read operations and ContextForge's four are live right now —
`cerberus connectors plugin managed exec contextforge get_health` returned
`{"ok":true,"status":"healthy"}` during this audit — and an agent cannot call
any of them. Worse, an agent that asks `cerberus_connector_describe azure` is
handed, as the documented invocation for every operation,
`cerberus connectors plugin managed exec azure list_subscriptions` — a CLI
command. The agent surface documents itself by pointing at a surface the agent
does not have.

The asymmetries run in both directions, which is why a per-surface audit misses
them:

- **MCP-only:** `namecheap`'s `get_dns_record_set` and `set_dns_record_set` have
  MCP tools and no CLI command — and `cerberus dns list`'s own help tells the
  operator to "use explicit set_dns_record_set" for authoritative whole-zone
  replacement. The response budget and progress notifications are also MCP-only.
- **CLI-only:** `ssh stop`, `forge get_deployment_script`,
  `forge update_deployment_script`, `resource show`, `project show`, daemon
  lifecycle, `run-secrets`, `path`, `init`, `write-plugin-prototype`, and the
  in-process `connectors plugin exec` path.
- **Console-only:** the entire `internal/infra` provider/deployment subsystem
  and the 24-hour overview trend — capability that exists only behind a browser,
  in a tool whose thesis is agent-first.
- **Socket-only:** NDJSON progress streaming, which reaches an MCP host because
  `SocketClient` re-emits notifications into the caller's context, and is
  silently dropped for CLI and console callers.
- **Neither CLI nor MCP:** `docker destroy` and `digitalocean status`, both
  declared by their connectors and reachable only through the generic route.
- **No surface but the CLI:** the whole plugin lifecycle. `connectors plugin
  managed install|load|unload|uninstall|health|exec` has CLI commands, socket
  routes, console HTTP routes and console UI — and zero MCP tools. An
  agent-first control plane cannot manage its own plugins from the agent
  surface.

One more inconsistency worth recording is that the surfaces do not agree on what
they *are*. The `cerberus web` process running on this machine (pid 25756,
started 15 Sep) renders `Bearer [REDACTED] — X-API-Key [REDACTED] a raw token
both 401` for the ContextForge token description, while the CLI, socket and MCP
served by the 17 Sep binary render `Bearer only — X-API-Key [REDACTED] a raw
token both 401`. Same field, two different corruptions, because a long-lived
console is a separately versioned surface that nothing supervises.

## The matrix

Legend: **Y** exposed · **n** not exposed · **~** partial or composed, see note.
"HTTP API" is the `cerberus web` JSON API on 127.0.0.1:4783; "Console" is the
React UI that sits on it. `cerberus mcp-http` serves the MCP column over HTTP,
not the HTTP-API column.

| Capability | CLI | Socket API | HTTP API | MCP | Console |
|---|---|---|---|---|---|
| health / daemon liveness | Y | Y | Y | Y | Y |
| resource list | Y | Y | Y | Y | Y |
| resource status / logs | Y | Y | Y | Y | Y |
| resource inspect / doctor | Y | Y | Y | Y | n |
| resource deploy / apply / reload / stop / sync / remove | Y | Y | Y | Y | Y |
| resource ensure-fresh | Y | ~ composed client-side, no route | n | Y | n |
| resource show (config view) | Y local-only | n | n | n | n |
| project list | Y | Y | Y | Y | Y |
| project show | Y | n | n | n | n |
| pipeline list / show / run | Y | Y | ~ list+run | ~ list+run | ~ list+run |
| connectors list / describe | Y | Y | Y | Y | Y |
| **generic connector operation exec** | **n** | **Y** | **Y** | **n** | **Y** |
| plugin managed list | Y | Y | Y | **n** | Y |
| plugin managed install (by path) | Y | Y | n (410) | **n** | n |
| plugin managed load / unload | Y | Y | Y | **n** | Y |
| plugin managed uninstall | Y | Y | Y | **n** | n |
| plugin managed health | Y | Y | Y | **n** | Y |
| plugin managed exec | Y | Y | Y | **n** | Y via connectors page |
| plugin exec / health (in-process, unmanaged) | Y | n (410) | n (410) | n | n |
| write-plugin-prototype | Y | n | n | n | n |
| validate / config validate | Y | n | Y | **n** | Y |
| config resolve | n | n | Y | n | Y |
| config backups / restore | n | n | Y | n | Y |
| config migrate | n | n | ~ 410 tombstone | n | n |
| register / deregister | Y | n | Y | **n** | Y |
| registry list / health | Y | ~ /registry/diagnostics | Y | **n** | Y |
| infra providers / deployments | **n** | **n** | **Y** | **n** | **Y** |
| overview 24h trend | **n** | **n** | **Y** | **n** | **Y** |
| settings / layout | ~ `path` | n | Y | n | Y |
| daemon start / stop / restart / status | Y | n | n | n | n |
| launch-agent install / uninstall | Y | n | n | n | n |
| init | Y | n | n | n | n |
| run-secrets | Y | n | n | n | n |
| progress notifications | **n** dropped | Y NDJSON | **n** dropped | **Y** | n |
| response budget / paging | n | n | n | **Y** | n |

Per-connector operation coverage, from `cerberus connectors describe <id>`,
which is authoritative:

| Connector | Ops | CLI | MCP | Socket + HTTP + Console |
|---|---|---|---|---|
| ssh | 5 | 5 | 4 — no `stop` | 5 |
| docker | 5 | 4 — no `destroy` | 4 — no `destroy` | 5 |
| github | 3 | 3 | 3 | 3 |
| cloudflare | 5 | 5 | 5 | 5 |
| namecheap | 6 | 4 — no record-set ops | 6 | 6 |
| digitalocean | 7 | 6 — no `status` | 6 — no `status` | 7 |
| forge | 7 | 7 | 5 — no script read/write | 7 |
| azure (plugin) | 6 | 6 via `plugin managed exec` | **0** | 6 |
| contextforge (plugin) | 4 | 4 via `plugin managed exec` | **0** | 4 |
| **total** | **48** | **44** | **33** | **48** |


## Correction from the second pass

The row above originally read `resource status / inspect / doctor / logs` as present on all five surfaces. That was wrong for two of the four verbs. The console's own HTTP API does serve `GET /api/resources/{id}/inspect` and `/doctor` — both are routed at `internal/webui/server.go:347-348` and both are covered in `server_test.go` — but `web/src/api/client.ts` has no method for either, so the console UI cannot reach them. The console serves two verbs its own interface never calls.

The cause is the same one this record identifies for MCP: the supervision lane has no declared operation metadata to generate a surface from, so each surface carries a hand-maintained verb list — five of them — and they have drifted. The connector lane does not have this problem because `connectors.tsx` iterates discovery metadata.

## Since P0

PR #50 made the plugin rows above true in a stricter sense. The one-shot
`plugin_dir` routes and web install-by-path now answer 410, so running a
directory is CLI-only and installing one is CLI-plus-socket. The generic
connector route is no longer free-form for ssh and docker (CERB-DEC-813), and
the MCP ssh and docker tools accept only a resource id. `docker destroy` is
still on neither the CLI nor MCP (CERB-GAP-272). The per-connector table
predates `ssh put_dir` and `get_dir`, which are on the CLI and MCP, so ssh now
declares seven operations rather than five.

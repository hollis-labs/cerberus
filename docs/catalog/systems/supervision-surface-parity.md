---
id: "CERB-CAP-830"
class: "capability"
name: "Supervision-lane surface parity"
summary: "The thirteen supervision verbs reach five surfaces unevenly and in the opposite direction from connector operations: the CLI is complete at 13/13, MCP reaches 12, the socket and HTTP APIs 11 each, and the console UI only 10."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "Every cell derived twice: verb list from the installed binary's command tree, socket and HTTP routes from their switch statements, MCP from the registration list in cmd_mcp.go, and the console column from web/src/api/client.ts rather than from the route table"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "docs/catalog/systems/supervision-surface-parity.md"
tags:
  - "cerberus"
  - "surface"
  - "parity"
  - "matrix"
  - "resource"
  - "supervision"
  - "console"
  - "agent"
  - "area:pass2-gaps-surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "the connector-operation matrix; this is its missing counterpart for the supervision lane"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "the lane whose verbs are being counted"
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
    note: "the HTTP API and console UI columns"
  - type: "blocks"
    target: "CERB-GAP-831"
    note: "the inspect and doctor routes the console UI never calls"
  - type: "blocks"
    target: "CERB-GAP-835"
    note: "the structural cause: five hand-maintained verb lists"
  - type: "relates_to"
    target: "CERB-GAP-449"
    note: "ensure-fresh, the verb with no route on any surface"
  - type: "relates_to"
    target: "CERB-GAP-448"
    note: "resource show, the verb that resolves config client-side"
  - type: "relates_to"
    target: "CERB-GAP-836"
    note: "MCP's conditional SSH tools, found while enumerating the MCP column"
---

# Supervision-lane surface parity

Cerberus's connector lane and its supervision lane have opposite surface
profiles, and pass 1 measured only the first. Where 48 connector operations
reach the socket API, the console HTTP API and the console UI mechanically —
the socket has one generic route, `POST /connectors/{id}/operations/{op}`, and
`web/src/pages/connectors.tsx` iterates `connector.operations` from discovery
metadata — the supervision lane has no generic route anywhere. Every one of its
thirteen verbs is a hand-written entry in a hand-maintained list, and there are
five such lists:

- `cmd/cerberus/cmd_resource.go` — one cobra subcommand per verb.
- `internal/cerbapi/socket_server.go`, `handleResourcesID` — a `switch action`.
- `internal/webui/server.go`, `handleResourceByID` — a second, independently
  written `switch action`.
- `cmd/cerberus/cmd_mcp.go` and again `cmd/cerberus/cmd_daemon.go` — one
  `NewCerberusResourceXxxTool` per verb, registered twice.
- `web/src/pages/resources.tsx` — an `ACTIONS` array of six buttons.

Five hand-maintained lists have drifted to four different lengths.

## The matrix

Legend: **Y** exposed · **n** not exposed · **~** partial or composed.
"HTTP API" is the `cerberus web` JSON API on 127.0.0.1:4783; "Console UI" is
what the React app actually calls, which is not the same set.

| Verb | CLI | Socket API | HTTP API | MCP | Console UI |
|---|---|---|---|---|---|
| `list` | Y | Y | Y | Y | Y |
| `status` | Y | Y | ~ bare `{id}` | Y | Y |
| `logs` | Y | Y | Y | Y | Y |
| `inspect` | Y | Y | Y | Y | **n** |
| `doctor` | Y | Y | Y | Y | **n** |
| `deploy` | Y | Y | Y | Y | Y |
| `apply` | Y | Y | Y | Y | Y |
| `reload` | Y | Y | Y | Y | Y |
| `sync` | Y | Y | Y | Y | Y |
| `stop` | Y | Y | Y | Y | Y |
| `remove` | Y | Y | Y | Y | Y |
| `ensure-fresh` | Y | **n** | **n** | Y | **n** |
| `show` | ~ resolved locally | **n** | **n** | **n** | **n** |
| **total** | **13** | **11** | **11** | **12** | **10** |

The direction of the asymmetry inverts. On connector operations the CLI (44 of
48) and MCP (33 of 48) are the weak surfaces and the console is complete; on
supervision verbs the CLI is complete at 13 of 13, MCP is nearly complete at 12
of 13, and **the console UI is the weakest surface at 10 of 13**. Anyone
planning from the connector-operation matrix alone would conclude that the
console never misses a verb — and for the lane an operator is most likely to
open a browser for, the opposite holds.

Three holes are worth naming, because each has a different cause.

**`inspect` and `doctor` are served and never called.** `handleResourceByID`
answers `GET /api/resources/{id}/inspect` and `GET /api/resources/{id}/doctor`,
and `web/src/api/client.ts` defines no method for either; a search of `web/src`
for `doctor` returns one unrelated `recommended_action` comparison and nothing
else. The console is the surface an operator reaches for when something looks
wrong, and it offers no diagnostic view at all. This is the cheapest gap on the
board — the routes, the DTOs and the token plumbing already exist — and it is
the one pass 1 recorded as present on every surface.

**`ensure-fresh` has no route**, because it is composed in the client from
status plus one of deploy/apply/sync. It therefore exists on exactly the two Go
consumers of `cerbapi` — the CLI and the MCP adapter — and on nothing else.

**`show` has no route either**, and resolves the config in the calling process,
so it reports the caller's registry view rather than the daemon's.

Two smaller observations belong here rather than in the connector matrix. The
HTTP API serves status at the bare `/api/resources/{id}` via
`GetResourceRuntime` rather than at a `status` sub-path, so a reader diffing
route tables between the socket and the console will see a verb that looks
missing and is not. And MCP's four SSH tools are registered inside an `else`
branch: if `loadUnifiedForTools` fails, `cerberus mcp` logs a warning and serves
49 tools instead of 53, with no indication to the agent that four are gone.
That is a client-side config resolution in a surface whose whole point is to be
a thin shell over the daemon.


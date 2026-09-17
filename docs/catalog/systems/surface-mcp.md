---
id: "CERB-CAP-402"
class: "capability"
name: "MCP adapter"
summary: "53 hand-registered MCP tools served over stdio and HTTP by a stateless RPC client to the daemon socket, covering 33 of 48 connector operations and none of the plugin lifecycle."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "Live tool list matches source exactly; two tools called against the running daemon; mcp-http not running so unverified"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/mcp/"
tags:
  - "cerberus"
  - "surface"
  - "mcp"
  - "agent"
  - "area:surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "the MCP column in the surface matrix"
  - type: "depends_on"
    target: "CERB-CAP-401"
    note: "every tool call re-dials the daemon socket"
  - type: "blocks"
    target: "CERB-GAP-430"
    note: "hand-maintained registration"
  - type: "blocks"
    target: "CERB-GAP-431"
    note: "the list is duplicated in the daemon"
  - type: "blocks"
    target: "CERB-GAP-432"
    note: "no plugin lifecycle tools"
  - type: "blocks"
    target: "CERB-GAP-433"
    note: "no plugin connector operation tools"
  - type: "blocks"
    target: "CERB-GAP-434"
    note: "no generic connector operation tool"
  - type: "implements"
    target: "CERB-TOOL-415"
    note: "the response budget"
  - type: "relates_to"
    target: "CERB-DEC-470"
    note: "why selfexec.WatchAndExit must not return"
---

# MCP adapter

Cerberus exposes 53 MCP tools. Verified live: the tool list in this audit
session contains exactly the 53 names the source registers, and
`cerberus_connector_list` and `cerberus_connector_describe` were called against
the running daemon and returned real data.

Two adapters serve the same tool set:

- **`cerberus mcp`** — stdio JSON-RPC, a thin RPC client that holds no config
  state of its own and forwards every call to the daemon socket. It pings on
  boot but does not hard-fail, because the MCP host may start it before the
  daemon exists.
- **`cerberus mcp-http`** — the same `buildCerberusMCPServer` over HTTP on
  `127.0.0.1:4785/mcp`, plus an unauthenticated `/health`. Nothing was listening
  on 4785 on this machine, so the HTTP adapter is `unverified`.

A third registration lives inside the daemon itself: `cerberus daemon` starts a
stdio MCP server against its `InProcessClient` for consumers that pipe stdio to
the daemon directly. Under launchd that goroutine reads a stdin nothing writes
to.

**The load-bearing finding: tool registration is hand-maintained, not
generated.** `buildCerberusMCPServer` is a flat list of 52 `RegisterTool` calls
naming 52 hand-written `NewCerberusXxxTool` constructors, and the daemon repeats
the same list inline. Nothing iterates connector metadata to produce tools. So
the premise that a new connector capability becomes a CLI command, an API
operation and an MCP tool at the same time is true for the API — the socket has
one generic operations route — and false for MCP, where it becomes a tool only
when somebody writes and registers one.

`AGENTS.md` names `external_connector_service.go` as the owner of "MCP tool
generation". It is not. Its only use of `go-mcp` is `NotifyMessage` /
`NotifyProgress` for progress streaming. Likewise `pkg/plugin`'s
`ToolNameForOperation` — documented as "MCP tool naming" — names the tool a
*plugin subprocess serves to the host* over `mcp/call_tool`. It is transport
between host and plugin, not a tool an agent can see.

The mechanism to do it properly is already in the repo and already works.
`NewCerberusDNSRecordSetTools` loops over `namecheap.Definition().Operations`,
filters to two names, and derives the tool name, description and input schema
mechanically from the connector metadata, adding `dry_run` and `acknowledged`
when the operation is destructive. It is applied to 2 of 48 operations.

MCP is also the only surface with two capabilities of its own: a response budget
(`limit`/`offset` with a 25-item cap and a `{items,count,total,truncated,hint}`
envelope) and progress notifications, which reach an MCP host through
`gmcp.Notify` and are silently dropped for CLI and console callers.


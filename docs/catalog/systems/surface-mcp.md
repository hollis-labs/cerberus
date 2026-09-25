---
id: "CERB-CAP-402"
class: "capability"
name: "MCP adapter"
summary: "56 MCP tools from one registry (mcp.AllTools), served over stdio, HTTP and the daemon's own stdio server, every one annotated from its operation's contract rather than by hand; plugin operations reach MCP as generated tools once the operator exposes them in connector-config.yaml, and the plugin lifecycle still has no tools."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "tool registration read at audit time; tool schemas, hints and mcp-http guard re-read on main after P0 (#48 to #54), the guard probed live on a scratch HOME"
last_reviewed: "2026-09-25"
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
  - type: "relates_to"
    target: "CERB-GAP-433"
    note: "plugin operation tools, now generated from the manifest"
  - type: "blocks"
    target: "CERB-GAP-434"
    note: "no generic connector operation tool"
  - type: "implements"
    target: "CERB-TOOL-415"
    note: "the response budget"
  - type: "relates_to"
    target: "CERB-DEC-470"
    note: "why selfexec.WatchAndExit must not return"
  - type: "relates_to"
    target: "CERB-DEC-810"
    note: "mcp-http's Host and Origin guard"
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "the ssh and docker tools send only a resource id"
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
- **`cerberus mcp-http`** — the same `buildCerberusMCPServer` over HTTP on a
  loopback address (default `127.0.0.1:4785/mcp`), plus `/health`. Since PR #48
  both sit behind a loopback Host and exact-Origin guard, and `--listen` refuses
  any non-loopback address, with no override (CERB-DEC-810, CERB-DEC-811). There
  is still no authentication, so any local process can call every tool
  (CERB-GAP-443). The guard and the listen refusal were probed live for the P0
  catalog pass, on a scratch `HOME`.

A third registration lives inside the daemon itself: `cerberus daemon` starts a
stdio MCP server against its `InProcessClient` for consumers that pipe stdio to
the daemon directly. Under launchd that goroutine reads a stdin nothing writes
to.

**The load-bearing finding: tool registration is hand-maintained, not
generated.** At audit time `buildCerberusMCPServer` was a flat list of 52 `RegisterTool` calls
naming 52 hand-written `NewCerberusXxxTool` constructors, and the daemon repeated
the same list inline. The P1-3 branch replaced both with `mcp.AllTools`; the
constructors are still hand-written. Nothing iterates connector metadata to produce tools. So
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

**Plugin operations are now generated** (CERB-GAP-433, 2026-09-25). A loaded
plugin's operation gets an MCP tool only when the operator lists it under
`<plugin id>: mcp: expose:` in `~/.cerberus/connector-config.yaml`, so exposure
is default-deny and installing a plugin never widens the agent tool surface by
itself. The generated tool is named `cerberus_<plugin>_<op>`. It takes the
manifest's input schema plus `dry_run` and `acknowledged` where the contract
calls for them, gets its hints from `contract.HintsFor` on the effective
contract (so a gap reads as exec), and runs through the admin lane. It is
refused if its name would shadow a hand-written tool, or if the manifest
declares an argument the host adds itself. `mcp.ServePluginTools` reconciles
the served list with the daemon every 15 seconds on all three servers. The
SDK sends `notifications/tools/list_changed` on each change, which a client
receives when it subscribed at connect, and it can do so because the
hand-written tools make the server advertise `tools.listChanged`.

MCP is also the only surface with two capabilities of its own: a response budget
(`limit`/`offset` with a 25-item cap and a `{items,count,total,truncated,hint}`
envelope) and progress notifications, which reach an MCP host through
`gmcp.Notify` and are silently dropped for CLI and console callers.

**P0 changed what the tools accept and how they are hinted.** The ssh tools send
only `resource_id` and operation fields. The docker tools dropped `docker_host`,
`docker_context` and `compose_file`, and `up`/`down` take `resource_id`. A
handler never forwards a target field even if an agent adds one (PR #50, PR #52;
CERB-DEC-813). PR #49 corrected the hints. `cerberus_droplet_create`,
`cerberus_droplet_stop`, `cerberus_resource_{stop,deploy,ensure_fresh,apply,sync,reload}`
and `cerberus_pipeline_run` are `DestructiveHint: true`. `cerberus_ssh_get` and
`cerberus_ssh_get_dir` are no longer `ReadOnlyHint`, because they overwrite a
local path. An MCP client keeps the schemas it loaded until it reconnects. The
hand-pinned hints this paragraph describes were replaced on the P1-3 branch by
derived ones (below).

## Since P1 (PRs #57, #60 and #63, and the P1-3 branch)

**One tool list.** `internal/mcp/registry.go`'s `AllTools(client)` is served by
`cerberus mcp`, `mcp-http` and the daemon's stdio server, which calls the
in-process client unmarked and so counts as a remote caller (CERB-DEC-817).
There are 56 tools; `cerberus_docker_destroy` is new.

**Derived annotations** (CERB-DEC-818). Every tool's four hints come from
`contract.HintsFor` on the operation it runs, applied by `mcp.WithHints`.
`internal/mcp/hints.go` binds each tool to its operation and is the only file
in the package that sets a hint; `TestNoHandWrittenHints` fails on one anywhere
else. The rules:

- ReadOnly is `!requires_ack`.
- Destructive is `requires_ack`.
- Idempotent is claimed only by reads.
- OpenWorld is true for a target outside Cerberus's own `local.`, `pipeline`
  and `cerberus.` kinds.

As a result, OpenWorld is now true for the provider, SSH and Docker tools, and
droplet start and docker up/down are destructive, matching their
acknowledgment. The control plane's own reads (health, project list, connector
discovery) have contracts too (`cerbapi.ControlPlaneDefinition`).

**Refusals are errors.** Since PR #57 every tool result that reports failure
sets `isError: true`, with the same redacted body. Every tool over an
operation that needs acknowledgment takes `acknowledged`: the resource
mutations, pipeline run, docker up/down/destroy, droplet start/stop/destroy,
ssh get/get_dir and the existing writes.

## Since P2-1

`cerberus mcp` claims, for every tool call, an agent via `mcp_stdio`, named by
the clientInfo in the call's `_meta`. Clients on the current protocol send it on
every request. The fallbacks are one captured at a legacy initialize handshake,
then `mcp-client` (`mcpPrincipal` in `cmd/cerberus/cmd_mcp.go`).
`cerberus mcp-http` claims `mcp_http` the same way, and the daemon adds the uid
from peer credentials. The daemon's own stdio MCP server labels its calls as an
agent via `mcp_stdio`. They stay unmarked as a surface, so they are still
remote (CERB-DEC-817).

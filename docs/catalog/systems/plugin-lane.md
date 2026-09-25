---
id: "CERB-CAP-301"
class: "capability"
name: "Plugin lane (subprocess connector plugins)"
summary: "Loads a standalone connector binary as a JSON-RPC subprocess from a validated plugin.yaml, and routes manifest operations to it as a first-class connector."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "manager, installer and policy re-read on main after P0 (#48 to #54); plugin inventory as observed at audit time"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/pluginhost/"
tags:
  - "plugin"
  - "connector"
  - "subprocess"
  - "pluginhost"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-304"
    note: "installs and launches what the authoring contract defines"
  - type: "depends_on"
    target: "CERB-CAP-302"
    note: "a plugin's credentials arrive over the Init config channel"
  - type: "depends_on"
    target: "CERB-CAP-306"
    note: "install records an origin (installed or dev) that every operation is checked against"
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "a loaded plugin id is dispatched ahead of the built-in registry in ExternalConnectorService.Execute"
  - type: "blocks"
    target: "CERB-GAP-330"
    note: "no plugin operation reaches MCP or gets a CLI verb"
  - type: "relates_to"
    target: "CERB-DEC-814"
    note: "a plugin dry run is forwarded only when declared, and is plugin-claimed"
  - type: "relates_to"
    target: "CERB-DEC-815"
    note: "origin replaced the trust tiers"
---

# Plugin lane (subprocess connector plugins)

The plugin lane is how Cerberus grows a provider integration without growing the
binary. A plugin is a standalone executable in its own Go module and its own
repository. It carries a `plugin.yaml` that pairs an entrypoint with a connector
manifest, and it speaks a small JSON-RPC protocol over stdin/stdout —
`plugin/init`, `plugin/load`, `plugin/unload`, `plugin/health`,
`mcp/call_tool`. The host installs it by reading and validating that manifest,
launches it as a child process with an allow-listed environment, resolves the
credentials the manifest declares and hands them over in `plugin/init`, and
thereafter routes operations to it by MCP tool name
(`cerberus_<connector>_<operation>`).

There are two hosts, and the difference matters. The **one-shot host** behind
`cerberus connectors plugin health` and `cerberus connectors plugin exec`
installs, loads, runs one call and unloads, in the CLI process. It registers
nothing and persists nothing. Since PR #50 nothing but a CLI process can reach
it: its socket and web routes answer 410, and the daemon no longer builds a
`PluginConnectorService` (CERB-GAP-846). The **daemon-managed host** behind
`cerberus connectors plugin managed …` keeps an inventory in
`~/.cerberus/plugin-connectors.json`, holds the subprocess open for the
daemon's lifetime, and restores the inventory at daemon start. A plugin loaded
into the managed host also becomes a connector for the whole admin lane: a
loaded plugin id is dispatched ahead of the built-in registry in
`ExternalConnectorService.Execute`, so `POST /connectors/<id>/operations/<op>`
over the daemon socket reaches it, with the same `credential_missing` error code
a built-in produces.

Two plugins exist and both are loaded on this machine, as children of the
daemon: `contextforge` (4 read operations against the ContextForge MCP gateway)
and `azure` (6 read operations against an Azure subscription). Both were v0.1.0,
both recorded as `trust_tier: unsigned` (since PR #51 that field is
`origin: installed`), and both were built from the separate plugins
repository. Both compile against
`github.com/hollis-labs/cerberus v0.4.0-beta.2` — the authoring contract they
were built against is a pre-release tag, not the tree the host runs. Runtime
compatibility is not left to that: `Manager.Load` compares the plugin's reported
protocol integer against `SDKProtocolVersion` and refuses a mismatch.

What the lane does *not* do is the interesting half. It does not supervise: the
manager holds the `Process` until something calls `Unload`, and nothing watches
for the subprocess dying, so a crashed plugin stays `loaded: true` and fails
every call. It does not version: the persisted inventory records a directory,
install options and a loaded flag — no version, no hash, no pin, so rebuilding
`dist/` and restarting the daemon silently swaps the implementation. It does not
reach every surface: despite the promise in both this repo's `AGENTS.md` and the
plugins repo's README that a manifest operation becomes "a CLI command, an API
operation and an MCP tool", no plugin operation has an MCP tool or a CLI verb of
its own — only the socket/HTTP API gives a plugin operation a first-class
endpoint.

Two host-side rules changed in PR #49, on both hosts, at their one shared choke
point, `pluginhost.Manager.ExecuteOperation`. The host demands `--ack` for any
operation the manifest marks `destructive`, whatever `requires_ack` says, so a
manifest can no longer opt out of the gate. And a dry run reaches the plugin only
for an operation that declares `supports_dry`; anything else is refused as
`preview_unsupported` without calling the plugin. A forwarded dry run is the
plugin's own claim, which the host cannot verify (CERB-DEC-814).

## Since PR #60

Plugin manifests carry the operation contract (CERB-CAP-212). An operation with
no `effect` loads, is treated as `exec`, and is reported in
`plugin managed list`'s `contract_gaps`, which is always present and `[]` when
there are none (CERB-DEC-821). A plugin's `input_schema` is enforced as its key
table on both the admin lane and managed exec. cerberus-plugins PR #5 (P1-6a)
declared an effect on every operation of our plugins, so none has a gap.

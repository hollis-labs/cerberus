# Plugin Capability Audit

A cross-application audit of the plugin extension surface: what
`hollis-labs/plugin-sdk` standardizes, what Cerberus uses, what Nanite uses,
and what Cerberus is leaving on the table. Findings and options, not a work
breakdown.

Run 2026-09-18 against `plugin-sdk` v0.4.0, Cerberus `b6fdc35`, and Nanite at
`~/Projects-apps/nanite`. Nanite's `go.mod` on the machine audited declares
`plugin-sdk v0.3.2-0.20260911214956-189243000161`; the SDK's current tag is
`v0.4.0`, so treat Nanite's numbers here as one minor version behind.

The point of the audit is alignment, not convergence. Two hosts using a shared
contract differently is fine; two hosts using it differently *by accident* is
the thing worth catching.

## What the SDK actually standardizes

`plugin-sdk` is host-neutral by design and says so: "Keep the base contract
host-neutral and dependency-free. The moment a host product's types appear
here, every other host inherits them."

**It standardizes:**

- The JSON-RPC 2.0 wire protocol and `ProtocolVersion` (pinned at 1).
- The lifecycle: `Init` / `Load` / `Unload`.
- Eight capability interfaces a plugin may implement: `Plugin`,
  `CommandHandler`, `EventHandler`, `HealthChecker`, `CRUDHandler`,
  `Migrator`, `MCPHandler`, `HTTPHandler`.
- `Serve` — dispatch, concurrency, panic recovery, signal shutdown.
- Config, data and cache helpers, and a stderr logger with secret redaction.
- `ErrCancelled`, the sentinel a pre-hook returns to veto an action.
- The registry wire contract, with a Go view and a TypeScript view.

**It deliberately does not standardize:**

- **The registration vocabulary.** Each host owns its own manifest schema in
  its own package. Contribution "kinds" in the registry contract are an open
  string with opaque JSON metadata, precisely so one host's envelopes are
  another host's something else.
- **Any host's trust or isolation model.** Stated explicitly in the SDK's
  `AGENTS.md`.

So the SDK is shared **at the wire and lifecycle level**. The extension
*vocabulary* is app-owned. That is a deliberate boundary and this audit does
not propose crossing it — see "The shared gap" for the one thing that might
belong on the shared side, and why it fits the existing pattern rather than
violating it.

## Two corrections this audit produced

Recorded because both were stated with more confidence than they deserved.

**The SDK surface is not over-built.** An earlier pass through Cerberus alone
found five capability interfaces the Cerberus host never dispatches and called
them phantom capabilities. They are not: Nanite dispatches `MethodCRUD*` (all
five), `MethodCommandExecute` and `MethodHTTPHandle`. Across the two hosts, 7
of 8 interfaces are in real use. "Unused by Cerberus" is the accurate framing.

**"A plugin should not provide middleware" was the wrong answer.** The same
earlier pass concluded that plugin-provided middleware is too much trust for
too little generality, and proposed inventing a narrow "policy provider" role
instead. The SDK already has the safe subset and Nanite already ships it: a
**cancellable pre-hook event**. Narrow input, typed veto, bounded by a timeout.
No new concept required.

## Dispatch comparison

| SDK interface | Nanite | Cerberus |
|---|---|---|
| `Plugin` (Init/Load/Unload) | yes | yes |
| `HealthChecker` | — | yes |
| `MCPHandler` (host calls tools on the plugin) | — | yes |
| `CommandHandler` | yes | no |
| `EventHandler` | yes | no |
| `CRUDHandler` | yes (all five methods) | no |
| `HTTPHandler` | yes | no |
| `Migrator` | no | no |

Cerberus and Nanite have almost disjoint slices. Nanite treats a plugin as a
**feature contributor**; Cerberus treats it as a **connector implementation**.
That difference is legitimate and explains most of the rest of this document.

Note the asymmetry in MCP: Cerberus calls tools *on* the plugin
(`MCPHandler`), while Nanite has the plugin declare an MCP server it exposes
(`MCPServerRegistration`) and proxies to it. Same protocol, opposite direction.

## Registration comparison

Nanite declares twelve registration types in
`nanite/internal/plugin/config.go`. Cerberus's `pkg/plugin.PluginYAML` declares
one block, `cerberus.connector`.

| Nanite registration | Cerberus equivalent |
|---|---|
| `Events` (subscribe to host events, with priority) | none — no event bus exists |
| `Commands` (slash commands with typed args) | none — CLI is generated from the connector manifest |
| `Crud` (plugin owns a resource type) | none |
| `HttpRoutes` | none |
| `McpServers` | inverted — see above |
| `Envelopes`, `CardRules`, `Panels`, `Slots`, `Components`, `Keybindings` | not applicable, Cerberus has no plugin-facing GUI |
| `AgentProfiles` | not applicable |

Nanite's manifest also carries metadata Cerberus has no equivalent for:

- **`NaniteCompat{min, max}`** — a host version range.
- **`LoadType`** — `auto` (tools available by default) or `opt-in` (hidden
  until explicitly enabled), plus per-tool `ToolOverrides`.
- **`Requires`** — declared dependencies on other plugins, MCP servers, features.
- **`Dependencies`**, and provenance fields: author, license, homepage,
  repository.

## Nanite's three extension mechanisms, and whether Cerberus wants them

### 1. Host events with cancellable pre-hooks — **yes, this is the one**

`Host.EmitPreHook` (`nanite/internal/plugin/events.go:593`) emits an event to
subscribed plugins and returns whether any of them cancelled it. A hook returns
`plugin.ErrCancelled` and the action does not proceed. Each hook is bounded by a
5-second timeout. Around 25 event types exist, from `message.sending` and
`context.assembled` to `plugin.installed` and `provider.error`.

**Why Cerberus wants it:** this is WP-S5's policy hook, already built. A
pre-hook on something like `connector.operation.executing` lets an operator's
own plugin veto an operation — a company approval service, a change-window
check, an internal allow-list — without Cerberus knowing anything about that
system. And post-events (`operation.completed`, `plugin.load_failed`) are
exactly what an audit-sink plugin would subscribe to, which folds WP-S1's
export story into the same mechanism instead of a second one.

**What Cerberus would need:** an event bus. There is none today. That is the
real cost, and it is not small — but the alternative designs proposed for
WP-S5 and WP-S1 each need a fan-out point anyway, so it is shared cost rather
than new cost.

**One caution.** A pre-hook that can veto is on the critical path of every
operation it subscribes to. Nanite's 5s timeout is the right instinct; for
Cerberus the fail-open/fail-closed question is sharper, because a hook that
times out during a destructive operation has to resolve to something and both
answers are defensible. Decide it explicitly rather than inheriting a default.

### 2. A filter registry — **probably, and it is closer to Cerberus's needs than it looks**

`FilterRegistry` (`nanite/internal/plugin/filter.go`) is a WordPress-style
named filter chain: `Register(name, pluginID, priority, fn)` and
`Apply(name, data, ctx)`, with `RemoveByPlugin` for clean unload. It also
carries `FilterView` and `stripForView`, which strips fields from a payload
according to the view it is destined for.

**That last part is the interesting bit.** `stripForView` is a *dynamic,
extensible* version of exactly what ADR 0003 does statically with DTOs. Cerberus
deliberately chose the static version, and should keep it for the core — an
allow-list that a plugin can extend is not an allow-list. But the pattern maps
cleanly onto three things Cerberus is already planning: redaction filters
(WP-S2), audit-record enrichment (WP-S1), and PII egress policy (WP-S7).

**Recommendation:** worth understanding, not worth adopting wholesale. If
Cerberus grows a filter chain it should be host-internal, with plugin-supplied
filters a separate and later decision — because a plugin that can rewrite a
tool result before an agent sees it is a context-poisoning vector (`ASI06`).

### 3. A mutable HTTP mux — **available, low priority**

`MutablePluginMux` lets plugins add and remove routes at runtime, namespaced by
plugin id. Cerberus has two HTTP servers that could host this (the daemon
socket, which is `net/http` over a unix listener, and the web console).

Low priority because nothing in Cerberus currently wants it, but worth knowing
it is a solved problem next door if a plugin ever needs an HTTP surface — for
example a webhook receiver for a connector.

### Also transferable, and cheaper than any of the above

**`LoadType: opt-in` and `ToolOverrides`.** A plugin's operations stay hidden
from the agent's tool surface until an operator enables them. This is the
cheapest agent-safety feature in either codebase: it shrinks the default blast
radius without any new machinery, and it is a natural fit for Cerberus, where
every plugin operation automatically becomes an MCP tool.

**A host compatibility range.** Cerberus's `PluginYAML` has no equivalent of
`NaniteCompat{min, max}`, so a plugin built against one host version loads into
any later one with nothing objecting. The entrypoint hash catches a *changed
binary*; it does not catch a *stale contract*. This matters as soon as plugins
come from outside the organization.

**Provenance metadata.** Author, license, repository. Currently absent from
Cerberus's manifest, and a prerequisite for any plugin catalog.

## Cerberus's own capability gaps

From the same audit, independent of Nanite:

- **`SSH_AUTH_SOCK` is granted to every plugin**
  (`internal/cerbapi/plugin_connector_service.go`). The comment above the
  allow-list says it "carries no credentials by design", but an agent socket is
  a live credential handle: any loaded plugin can authenticate as the operator
  to every host that trusts their key. `DOCKER_HOST` / `DOCKER_CONFIG` are in
  the list too, and Docker socket access is root-equivalent on most hosts.
  These are present for a real reason — remote Docker over SSH needs them — but
  they are granted **ambiently, to every plugin, undeclared**, which is exactly
  the pattern the manifest-declared secret channel exists to avoid.
- **The sandbox is declared but unimplemented.** `SandboxEnforced` is never set
  true, and `internal/pluginhost/policy.go` refuses a plugin that requests a
  profile. Honest — it fails closed rather than pretending — but the capability
  is zero.
- **No per-call deadline and no resource limits.** `CallTool` passes the
  caller's context through and nothing sets a timeout; there is no rlimit,
  cgroup or memory cap. Only a 3-second *close* timeout exists. A plugin that
  hangs hangs the request.
- **`DataDir` / `CacheDir` are never populated**, so the SDK's
  `ResolvedDataDir()` returns `ErrNoDataDir` — and the SDK tells plugins to
  treat that as fatal for persistence. A contract the host declares and does
  not honor.

## The shared gap

**Neither host has a capability or permission declaration.** Nanite's manifest
has no `Permission`, `Capability`, `Scope` or `Grant` field either. So the
`SSH_AUTH_SOCK` finding is not a Cerberus mistake — it is a shared gap in how
both hosts treat plugin privilege.

That is worth naming because it changes where the fix belongs. A capability
*mechanism* is a candidate for the shared SDK; the capability *names* and their
enforcement are not.

And there is a precedent in the SDK for exactly that split: the registry
contract already treats a contribution kind as "an open string and its metadata
is opaque JSON, because one host's envelopes, widgets and slots are another
host's something else." A capability declaration can work the same way — the
SDK carries *that a plugin requests named capabilities*, and each host owns what
the names mean and whether to grant them.

This is proposed to the SDK rather than built here. See
`docs/proposals/capability-declaration.md` in `hollis-labs/plugin-sdk`.

## Options for Cerberus

Ranked by value for letting other people run this safely at work.

**A — Capability declaration and enforcement.** Make the ambient grants
declared and per-plugin. Highest value; blocked on, or at least better done
with, the SDK-side mechanism above. Interim version: keep the allow-list but
gate `SSH_AUTH_SOCK` and the Docker variables behind a manifest opt-in that
`plugin managed list` displays.

**B — `LoadType: opt-in`.** Cheapest real safety win. Borrow the field name
from Nanite so the two manifests agree where they overlap.

**C — Per-call deadlines and resource limits.** Small, independent, and removes
a hang from the daemon's critical path.

**D — Host compatibility range plus provenance metadata.** Small, and a
prerequisite for third-party plugins.

**E — An event bus with pre-hooks.** The big one. Unlocks plugin-supplied
policy (WP-S5) and audit export (WP-S1) through one mechanism, and aligns
Cerberus with Nanite on the most useful extension point either has. Do it after
WP-S1 so the audit record exists to record hook decisions in.

**F — Populate `DataDir` / `CacheDir`.** Trivial, and closes a declared
contract the host currently breaks.

**Not recommended:** plugin-supplied filters that rewrite tool results before
an agent sees them, and a general plugin middleware chain. Both put a
third-party subprocess in the path of everything, and the pre-hook veto covers
the legitimate cases with far less exposure.

## Sequencing note

B, C, D and F are all small and independent — a reasonable single pass. A wants
the SDK conversation first. E is the largest and should follow WP-S1 from
`agent-authority-and-secrets.md`.

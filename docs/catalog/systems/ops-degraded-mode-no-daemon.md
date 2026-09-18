---
id: "CERB-CAP-710"
class: "capability"
name: "Degraded mode: operating with the daemon down"
summary: "Defines what each surface does when the unix socket does not answer: reads fall back in-process, the connector lane picks its transport before executing, the plugin and MCP surfaces fail with a recovery instruction, and a third of the CLI never needed the daemon at all."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.92
confidence_label: "Every fallback site read directly and tabulated; the unreachable error text reproduced through redact.Text; not exercised by stopping the live daemon, which was out of scope"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/socket_client.go:490-497, cmd/cerberus/cmd_transport.go, cmd/cerberus/cmd_resource.go"
tags:
  - "cerberus"
  - "class:capability"
  - "degraded-mode"
  - "availability"
  - "daemon"
  - "cli"
  - "transport"
  - "cross-cutting"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-400"
    note: "the CLI is where almost all of the fallback logic lives"
  - type: "relates_to"
    target: "CERB-CAP-401"
    note: "the socket is the thing that is absent"
  - type: "relates_to"
    target: "CERB-CAP-402"
    note: "cerberus mcp has no fallback and re-dials per call instead"
  - type: "relates_to"
    target: "CERB-CAP-403"
    note: "the console proxies the socket, so its API routes fail while the daemon is absent"
  - type: "relates_to"
    target: "CERB-CAP-106"
    note: "the self-mutation guard is inert on the in-process path"
  - type: "relates_to"
    target: "CERB-CAP-711"
    note: "an in-process mutation is serialized by neither opMu nor the build lock"
  - type: "relates_to"
    target: "CERB-CAP-700"
    note: "the recovery instruction survives redaction, which four others did not"
  - type: "implements"
    target: "CERB-DEC-741"
    note: "reads prefer the daemon and fall back in-process"
  - type: "relates_to"
    target: "CERB-DEC-471"
    note: "cerberus mcp holds no state and re-dials, which is why it survives a restart"
  - type: "relates_to"
    target: "CERB-GAP-448"
    note: "resource show is local unconditionally, not as a fallback"
  - type: "relates_to"
    target: "CERB-GAP-730"
    note: "the classification that triggers the fallback is too broad"
  - type: "relates_to"
    target: "CERB-DEC-475"
    note: "connector liveness degrades deliberately when the daemon cannot answer"
---

# Degraded mode: operating with the daemon down

Every Cerberus surface is a client of `~/.cerberus/cerberus.sock`, so "what
happens when the daemon is down" is a question about all of them at once. The
answer is not one answer: three distinct behaviours are in the tree and each
was chosen on purpose.

`SocketClient` does not dial eagerly — the first RPC is the probe. When that
RPC's transport fails, `doJSON` and `doJSONStream` return
`DaemonUnreachableError`, whose text is the recovery:

    cerberus daemon not running at ~/.cerberus/cerberus.sock;
    start with 'cerberus daemon' (dial unix …: connect: connection refused)

Verified on 2026-09-17 that this string passes through `redact.Text`
byte-for-byte unchanged, so the one instruction an operator needs is not eaten
by the safety net that has eaten four others.

**Reads prefer the daemon and fall back in-process.** `resource list`,
`status`, `inspect`, `doctor`, `logs`, `project list`, `project show`,
`pipeline list` and `pipeline show` all try the socket, match
`DaemonUnreachableError`, and re-run the same call against a locally
constructed `ResourceRuntimeService`. The comment on `projectClient` states the
policy: prefer the daemon, fall back in-process, and take the trailing
resolve-diagnostics notice from whichever surface served the rows so the notice
describes the same view. `connectors` and `connectors describe` fall back too,
but deliberately degrade: the merged definition list survives and liveness
reverts to the calling shell's view, which the code comment names as exactly
how a connector can read `LIVE=yes` while every call against it fails.

**The connector lane chooses its transport before executing.** `commandSocket`
pings with a 2-second budget and routes in-process only if that ping comes back
unreachable; `newExternalConnectorService` then hands back either a socket
executor or `app.NewExternalConnectorService()`. Its comment states the
invariant: "Once an operation is sent, an error must never trigger an
in-process retry of a possible mutation." So `cerberus ssh exec`,
`cerberus docker ps` and the rest work with no daemon, at the cost of running
under the operator's `PATH` and environment rather than launchd's.

**Some surfaces have no fallback, correctly.** All seven
`connectors plugin managed …` verbs go straight to a `SocketClient`, because a
daemon-managed plugin host cannot exist without the daemon. `cerberus mcp` is a
thin RPC client that holds no state and re-dials per call (CERB-DEC-471), so
every tool call fails with the unreachable error and the subprocess survives to
succeed once the daemon returns. `cerberus mcp-http` pings at boot, warns on
stderr, and keeps listening. `cerberus web` has `--wait-for-daemon` for the
startup race; its `/api/*` routes fail while the daemon is absent.

**And a large part of the CLI never needed the daemon.** `path`, `init`,
`install`, `uninstall`, `validate`, `config validate`, `register`,
`deregister`, `registry list`, `registry health`, `run-secrets`, `completion`,
`connectors plugin exec`, `connectors plugin health`,
`connectors write-plugin-prototype` and the `daemon` verbs themselves are all
local. `resource show` is local too, but unlike the rest it is local
unconditionally and never asks the daemon at all (CERB-GAP-448).

What the transport switch changes is the part worth knowing before relying on
it. The serving-daemon self-mutation guard is armed by `ProtectServingDaemon`,
which has exactly one non-test caller — the daemon itself — so an in-process
run has `servingDaemon == false` and the guard returns `nil` for every
resource. The daemon's `opMu` no longer serializes anything, because the
mutation is happening in a different process. And no surface reports which
transport served a result except indirectly, through the resolve-diagnostics
notice.

Marked `partial` for one reason: the fallback is the right design for reads and
was extended to mutations without being re-scoped, and the classification that
triggers it is broader than "the daemon is down" (CERB-GAP-730).

## What it owns

- `DaemonUnreachableError` and the recovery instruction it carries
- the read-path daemon-first, in-process-second policy across resource,
  project and pipeline commands
- transport selection before execution in the connector lane (`commandSocket`)
- the deliberate liveness degradation in `connectors` when the daemon cannot answer
- `cerberus mcp`'s per-call re-dial, which is what lets it outlive a daemon restart

## What it does not own

- the self-mutation guard, which is inert off the serving path by design (CERB-CAP-106)
- serializing an in-process mutation against the daemon's monitor (CERB-CAP-711)
- telling the caller which transport answered
- restarting the daemon: `daemon start` and launchd's `KeepAlive` do that

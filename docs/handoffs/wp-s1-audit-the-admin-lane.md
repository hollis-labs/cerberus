# Handoff — WP-S1: Audit the admin lane

**Parent:** `docs/plans/agent-authority-and-secrets.md` (WP-S1)
**State:** not started. `main` at `b6fdc35`.
**Prerequisite:** none. This is startable immediately and blocks WP-S5.

Take this whole package. It is one coherent change and splitting it produces a
half-audited system, which is worse than none because it reads as coverage.

## Why this first

`LogAudit(operation, serviceID, reason, taskID, sessionID)` exists in
`internal/service/lifecycle_log.go:52` with **zero callers**. Nothing records
who asked for an operation, which credential it resolved, what it targeted, or
what came back.

Cerberus reaches processes, remote hosts over SSH, container daemons, DNS, VPS
providers and a Kubernetes API server, and an agent drives all of it. The
failure modes that matter — OWASP `ASI03 Identity & Privilege Abuse`,
`ASI10 Rogue Agents` — are cases where the tools worked exactly as designed.
Those are detected through attribution, not prevented by design. Right now they
could not be detected at all.

## Do we use middleware? Yes, and it is the wrong place for the record

This was the first question asked of this work, so the answer is recorded here
rather than rediscovered.

**Middleware already exists in two places:**

- `internal/cerbapi/socket_server.go:182` — `SocketServer.wrap(mux)`. The daemon
  socket is **`net/http` over a unix listener**, not a hand-rolled protocol, so
  ordinary `net/http` middleware applies. `wrap` already negotiates an API
  version header (`APIHeaderName`) and logs method and path.
- `internal/webui/server.go:104` — `Server.withLogging`.

`cerberus mcp` over stdio re-dials the socket per tool call, so MCP traffic
arrives as HTTP too. That means one middleware on the socket server sees the
CLI-over-socket, MCP and web-console paths.

**It still is not where the audit record belongs, for two reasons.**

1. **Middleware cannot see what the record needs.** At the HTTP layer you have
   method, path, status and duration. You do not have the connector, the
   operation, the target host, which credentials were resolved, whether
   acknowledgment was honoured, or whether a dry-run short-circuited. Those
   exist only inside the service. A record built from method and path is a
   request log, and we already have one.
2. **It misses the in-process path entirely.** `cmd_transport.go:57` returns
   `app.NewExternalConnectorService()` and the CLI calls `svc.Execute` directly
   — no HTTP involved. This is not an edge case: AGENTS.md's
   `DaemonUnreachableError` invariant exists precisely so a mutation can re-run
   in-process. An audit layer that covers everything except the fallback path
   is blind exactly when things are already going wrong.

**So: two layers, with distinct jobs.** This is the design, and it is the part
to get right.

| Layer | Job | Must not do |
|---|---|---|
| HTTP middleware (`SocketServer.wrap`, and a new chain for `mcp-http`) | Read caller identity off the request, put it in the `context.Context`. Later: host the policy gate (WP-S5) and the request-scoped redactor (WP-S2). | Write the audit record. |
| Service layer (`ExternalConnectorService.Execute`, the six resource mutators) | Write the authoritative record: execution facts plus the caller identity from context. | Try to infer the caller. |

### The gap that decides whether this is real

**There is no caller identity on the socket wire.** `SocketClient` sends no
actor, session or origin header, so from the daemon's side every request is
anonymous. Audit that cannot distinguish an agent-driven destructive operation
from a human at a terminal does not answer the question audit exists for.

So this package includes adding it:

- A caller-identity header, following the `APIHeaderName` precedent already in
  `wrap`. Carry at least a **surface** (`cli`, `mcp`, `webui`, `http`) and an
  opaque **session id** where one exists. `LogAudit`'s existing signature
  already has `sessionID` and `taskID` fields, so the shape was anticipated.
- `SocketClient` sets it on every request.
- The **in-process path sets the same context values directly**, via one shared
  helper that the middleware and the CLI root both call. One function, two
  callers, so the two paths cannot drift.
- **Treat the header as a hint, not an authentication.** Anything that can
  reach the socket can set it. It is useful for attribution between our own
  surfaces and worthless against a hostile local process; record it as
  self-reported and do not build a control on it. Real caller authentication is
  WP-S8 and WP-S9 territory.

## Where the record must be written

### The admin lane — one chokepoint, which is the good news

`ExternalConnectorService.Execute` (`internal/cerbapi/external_connector_service.go:143`)
is the **single** entry point for every connector operation on every surface.
Built-ins, plugin connectors and managed plugins all funnel through it. Audit it
there and the whole admin lane is covered.

Cover every exit, not just the happy path. `Execute` can return at:

- the availability check
- the dry-run short circuit (`dryRunPreview` returning `ok=true`)
- the acknowledgment gate (`requireAcknowledgment`, line 249)
- the per-connector `execute<X>` dispatch, success or failure
- payload decode failure

A dry-run and a refusal are both outcomes worth recording. A refused
destructive operation is arguably the most interesting record in the log.

### The supervision lane — six mutators

`internal/cerbapi/resource_runtime_service.go`, no single chokepoint:

| Method | Line |
|---|---|
| `ReloadResource` | 637 |
| `StopResource` | 668 |
| `DeployResource` | 703 |
| `ApplyResource` | 920 |
| `SyncResource` | 999 |
| `RemoveResource` | 1054 |

Reads (`ListResources`, `GetResourceRuntime`, `GetResourceInspect`,
`GetResourceDoctor`, `Health`, `ResourceLogs`, `ListProjects`,
`ResolveDiagnostics`) do not need a record in this package. `ResourceLogs` is
the one to think about: it returns free text that can carry credentials and
PII, so it belongs in WP-S7's labelling rather than here — but note it, do not
silently skip it.

## Enforce it as a contract

Four mechanisms, in descending order of how much they actually prevent. Do all
four; the tests are the only ones that survive a refactor by someone who has
not read this document.

**1. Make the sink a required constructor dependency.**
`NewExternalConnectorService` and `NewResourceRuntimeService` take an audit sink.
No setter, no default that silently discards. A nil sink is a programming error
that fails at construction, not a quiet no-op at runtime. This is what stops the
next person forgetting.

**2. Funnel construction.** `cerbapi.NewExternalConnectorService` is currently
called from `internal/app/app.go:89`, `app.go:115`, `cmd_daemon.go:506` and
reached via `cmd_transport.go:57`. Add a `forbidigo` rule banning direct
construction outside `internal/app`, so every path gets the audited service and
a new caller cannot accidentally build a bare one.

**3. A classification test over the `Client` interface.** This is the structural
one. `internal/cerbapi/client.go` is the interface every caller uses. Write a
test that reflects over its method set and requires each method to appear in
exactly one of two explicit lists — `audited` or `readOnly`. A new method added
to `Client` then **fails the build until someone classifies it**. That converts
"remember to audit new operations" from a habit into a compile-time-ish gate,
which is the only version that holds.

**4. Per-operation coverage.** Enumerate `Definitions()` and assert that every
declared operation, executed against a fake connector, produces exactly one
record — including the destructive ones under refusal and dry-run. `fakeSSHBackend`
in `internal/cerbapi/external_connector_service_test.go` is the model.

## Fail closed, and where

The record has to be written **before** a destructive operation executes, not
after, or a crash mid-operation leaves the most important case unrecorded.

Recommended split, stated so it is a decision rather than an accident:

- **Destructive operation, audit write fails → refuse the operation.** This is
  the fail-closed case. It means the sink must be synchronous on this path.
- **Read operation, audit write fails → log loudly and proceed.** Refusing
  every read because a log file is unwritable turns an audit outage into a total
  outage, and reads are the operations an operator needs when something is
  already broken.

Write an intent record before execution and an outcome record after, correlated
by id, rather than one record after the fact. An operation that started and
never finished is a thing you want to be able to see.

## The record itself is a DTO

ADR 0003 applies to the audit record exactly as it applies to a tool result.

**Do not capture arguments verbatim.** `ExternalConnectorOperationArgs.Config`
is a `map[string]any` carrying whatever the caller passed, which for some
operations includes credential material. An audit log built by serialising the
request becomes the credential store nobody meant to build, with worse handling
than the real one and a longer retention period.

- Allow-list the fields recorded. Add fields deliberately.
- Credentials appear as **names only** — which credential *names* were resolved,
  never values. This is the `probe-*` convention the rest of the repo follows.
- Every field goes through value-boundary redaction (`redact.New(values...)`),
  not `redact.Text`. If WP-S2 has not landed, use `redact.New` here anyway; it
  already exists and this is one of the two callers it should have had.
- A test populates a sentinel credential into config and asserts it appears
  nowhere in the serialised record. Copy the pattern from
  `kubernetes/internal/k8splugin/dto_test.go` in `cerberus-plugins`, which does
  exactly this against live objects.

## Storage

Not prescribed, deliberately — but the constraints are:

- Survives a daemon restart. `slog` to the lifecycle log is the cheapest thing
  that meets this and `LogAudit` already writes there; it is a defensible v1.
- Append-only in intent. Do not build something the daemon rewrites in place.
- Queryable enough to answer "what did this session do" and "who touched this
  host". If `slog` output cannot answer those, that is the argument for the
  sqlite store the repo already depends on (`modernc.org/sqlite`).
- Decide retention explicitly and write it down. An audit log with no retention
  policy becomes an indefinite record of every credential name and target host
  on the machine.

## Acceptance

- Every `Client` interface method is classified `audited` or `readOnly`, and a
  new method fails the test until classified.
- Every operation in `Definitions()` produces exactly one record; destructive
  refusals and dry-runs produce records too.
- A destructive operation whose audit write fails is refused; a read whose audit
  write fails proceeds with a loud log.
- A sentinel credential placed in operation config appears nowhere in any record.
- Caller surface is recorded correctly for: CLI over socket, CLI in-process
  fallback, `cerberus mcp`, and the web console. Four paths, verified
  individually — the in-process one is the one that will be missed.
- `make test` and `golangci-lint run --new-from-rev=main ./...` clean, plus
  `gofmt -l .` separately (`--new-from-rev` does not check formatting).

## Do not

- Do not write the record in HTTP middleware. See above.
- Do not build the policy engine here. WP-S5 consumes this; the audit record
  gains a "policy decision" field then, and doing both at once makes the
  decision untestable.
- Do not add a caller *authentication* scheme. The identity header is
  self-reported by design in this package, and pretending otherwise is worse
  than the honest version.
- Do not touch `redact.Text`'s regexes. Seven patches is the finding; this
  package uses the value-boundary API instead.

## Working notes

Take a worktree — `git worktree add ../cerberus-wp-s1 -b feat/audit-admin-lane`.
Multiple sessions share this checkout and a bare `git add -A` sweeps another
session's staged work; this has already produced a commit that did not compile.
Commit with explicit pathspecs.

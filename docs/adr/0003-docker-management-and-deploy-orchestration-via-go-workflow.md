# ADR 0003: Docker Management and Deploy Orchestration via go-workflow

## Status

Proposed

## Date

2026-09-15

## Context

Chrispian is scaling up Docker usage across personal apps/dev and a work
environment reachable over VPN/SSH tunnels, and wants Cerberus to be the
control plane for both: manage images/configs, monitor running containers
(local always-on, remote on-demand), and orchestrate deployments. The work
side will likely add a corporate Jenkins pipeline and an enterprise GitHub
instance; AWS and DNS management are handled elsewhere and out of scope here.
MVP boundary: SSH-reachable hosts only. GUI is a separate discussion.

Survey of the current codebase found:

- A working, CLI-backed Docker connector (`internal/connector/docker/`) that
  manages containers/compose stacks that already exist — no image build/push,
  no `Create`, no remote-host awareness (shells to a local `docker` binary via
  `PATH` only).
- A real, secure SSH connector (`internal/connector/ssh/`) — host-key
  verification, keychain-backed keys, `Destructive`/`SupportsDry`-gated exec —
  the right primitive for reaching VPN/tunnel hosts on demand.
- An existing credential pattern (`connector-secrets.yaml`,
  `keychain://`/`helper://` references) already used by cloudflare/namecheap,
  directly reusable for registry credentials.
- A live, config-driven pipeline system (`pipelines:` in `*.cerberus.yaml`,
  `internal/pipeline/`, exposed via `cerberus pipeline list/show/run` and
  `cerberus_pipeline_*` MCP tools). This is real, used surface — not
  scaffolding — and is itself a descendant of the original Hadron pipeline
  engine.
- A precedent worth not repeating: `internal/infra/deploy.go`, a bounded,
  single-purpose deployment runner for Vercel, deliberately kept outside the
  connector model because Vercel doesn't decompose into local verbs. Docker +
  SSH do decompose into discrete connector verbs (build, push, exec), so that
  precedent does not apply here.
- `go-workflow` (`github.com/hollis-labs/go-workflow`), the workflow engine
  extracted from Hadron: durable graph-visible compensation (SAGA), suspend/
  resume waits, typed values/artifacts, verification, memoization, and an
  MCP-native step kind. Already adopted by Nanite for Agent Workflows and
  holding up in production there. Its own docs name Cerberus as an
  anticipated downstream consumer.

Cerberus's bespoke `internal/pipeline` DAG has none of go-workflow's
durability, compensation, or suspend/resume properties, and duplicates work
that Hadron's extraction already hardened.

## Options considered

- **Docker deploy shape:** a bounded, Vercel-style deployment runner outside
  the connector model, vs. extending the Docker connector with real
  `build`/`push`/`create` operations. Rejected the runner shape — Vercel
  needed it because it doesn't decompose into local verbs; Docker + SSH do.
- **Composition layer:** keep extending `internal/pipeline`, vs. adopting
  `go-workflow`. Rejected extending the bespoke engine — it has no durable
  compensation, no suspend/resume, no verification/memoization, and
  `go-workflow` already solves all of that, proven in Nanite.
- **Durable state backing:** extend `internal/store/sqlite` in place, vs. a
  new `go-sqlite`-backed store, vs. jumping straight to Postgres. Rejected
  extending the existing store — it sets WAL via a startup `db.Exec` against
  a pooled connection, the exact per-connection-pragma bug `go-sqlite` exists
  to fix. Rejected Postgres now — not ready per current timeline, unwarranted
  ops burden for a single-operator MVP.
- **Wait/callback endpoint termination:** a new dedicated listener, vs.
  reusing the existing Cerberus HTTP API, vs. no inbound endpoint at all
  (poll-only). Rejected a dedicated listener as duplicate auth/ops surface;
  rejected poll-only as the default since it loses durable-suspend
  cleanliness where a callback is possible.
- **MVP step-kind profile:** a bespoke go-workflow step kind per connector,
  vs. routing everything through the `mcp` adapter against Cerberus's
  existing MCP tools, vs. a hybrid. Rejected bespoke-per-connector as
  duplicate integration surface; rejected mcp-only as too rigid for cases
  needing native gate/wait semantics or tighter schemas.
- **Pipelines migration:** leave `pipelines:` on the old engine indefinitely,
  vs. transitional dual support, vs. migrating it in the same effort.
  Rejected dual support — two pipeline engines live at once was named
  explicitly as a standing source of confusion, not a transition worth
  having.

## Decision

1. **Extend the Docker connector** with real `build`, `push`, and `create`
   operations. Registry credentials resolve through the existing
   `connector-secrets.yaml` pattern.
2. **Leave the SSH connector as the generic remote-exec primitive** — no
   docker-specific knowledge added to it.
3. **Replace `internal/pipeline` with `go-workflow`** as the composition
   layer for any cross-connector or multi-step flow (build → push → remote
   apply; future Jenkins/GHE trigger flows). **Existing `pipelines:`
   definitions migrate to go-workflow's graph-native format in the same
   effort** — `internal/pipeline`, `cmd/cerberus/cmd_pipeline.go`, and
   `internal/mcp/tools_pipeline.go` are retired together, not run in
   parallel with the new engine.

Sub-decisions settled during review:

- **Durable state:** a new schema/store for go-workflow's `runtime.StateStore`
  and go-scheduler's schedule/fire `Store`, opened via `go-sqlite`'s
  `sqlitekit` (correct WAL-per-connection, single-writer pool), separate from
  the existing domain store, behind an interface narrow enough for Postgres
  to swap in later. `github.com/hollis-labs/go-queue` (driver-based,
  Laravel-style job queue) is a candidate for the `Runner`/dispatch seam
  behind `go-scheduler` and should be evaluated alongside it; a better
  community Go library for either problem is also acceptable.
- **Timer/activation scheduling:** `go-scheduler` — already hardened via
  Hadron's use of it (durable fire identity, CAS claims, crash recovery) —
  implements go-workflow's `wait.ActivationScheduler` seam. Business-logic
  wiring is the remaining work, not the library itself.
- **Wait/callback endpoints:** terminate on Cerberus's existing HTTP API as a
  narrow, authenticated route family, mirroring Nanite's pattern (Basic Auth
  + `Idempotency-Key` + persisted responder provenance). The literal
  transport stays open pending Cerberus's planned move to the official Go MCP
  SDK plus Unix-socket transport work; if that lands something that fits
  better than bare HTTP, prefer it, and treat the pattern as shareable across
  other apps rather than Cerberus-only.
- **Nanite lessons adopted now:** pin exact engine/plan/StepKind-catalog
  identity per run (resume never resolves "latest"); require an
  `Idempotency-Key` on every external-trigger endpoint from day one.
  Deferred: "ambiguous external effect" handling (Nanite's outbox/receipt
  pattern for at-least-once external calls) until a real fire-and-forget
  trigger exists to build it against (e.g. a Jenkins webhook).
- **MVP step-kind profile:** `cmd`, `http`, `mcp`, `gate`, `wait`. Docker/SSH
  operations route through the `mcp` adapter against Cerberus's own existing
  MCP tools (`cerberus_docker_*`, `cerberus_ssh_exec`) rather than getting
  bespoke step kinds; a bespoke step kind is reserved for cases the `mcp`
  adapter genuinely can't express. `agent`/`llm`/`checkpoint`/`script`/
  `service`/`call`/`transform`/`emit` are deferred, no decision needed yet.

## Implications

### Operator surfaces

CLI, MCP, and the resource-runtime service continue to be the surfaces;
docker `build`/`push`/`create` and any go-workflow-backed pipeline commands
extend the existing CLI/MCP patterns rather than introducing new ones.
`cerberus pipeline ...` and `cerberus_pipeline_*` continue to exist as the
operator-facing names, now backed by go-workflow instead of
`internal/pipeline`.

### Runtime model

Cerberus becomes a **host** for `go-workflow` per its adoption contract and
must supply: a durable `StateStore` (§ Decision, above), a
`wait.ActivationScheduler` via `go-scheduler`, `wait.Materializer` /
`wait.ResponderAuthorizer` on the existing HTTP API, a frozen
`stepkind.Registry` for the MVP profile, policy/authorization hooks, and
artifact/secret resolution wired to the existing `internal/secretref` /
keychain / `connector-secrets.yaml` machinery. `runtime/inmemory` is
explicitly not a production claim; a real durable host implementation is
required, qualified against go-workflow's own conformance suites
(`RunRequired`/`RunComplete`/`RunExhaustive`).

### Code organization

`internal/pipeline/*`, `internal/domain/pipeline.go`,
`internal/cerbapi/pipeline_service.go`, `internal/mcp/tools_pipeline.go`, and
`cmd/cerberus/cmd_pipeline.go` are retired and replaced by a go-workflow-backed
equivalent, in the same effort as the `pipelines:` config migration. Current
callers (at minimum `internal/connector/local/connector.go` and parts of
`internal/webui/*`) need an inventory pass before this is scoped into
sequenced work — not solved by this ADR.

## Consequences

### Positive

- Real, durable, graph-visible SAGA compensation replaces best-effort,
  in-process `Action.Rollback()` with no crash recovery.
- Durable suspend/resume across daemon restarts — fits "wait for a Jenkins
  build," "wait for an on-demand VPN host to become reachable," and "wait for
  a human approval" with one primitive.
- Approval gates become first-class graph nodes instead of scattered
  CLI-flag checks.
- Typed values/artifacts with provenance replace loose `map[string]any`
  config blobs passed stage to stage.
- Verification/memoization echoes the existing local-artifact
  content-hash staleness pattern (`artifact_stale`), now formalized at the
  engine level.
- The `mcp` step kind means most future integrations (Jenkins, GH
  Enterprise, eventually AWS) compose as "point a step at an MCP server"
  rather than requiring a bespoke Cerberus adapter each time.
- One pipeline concept instead of two — no standing "legacy vs. new" split
  to reason about or explain.
- Already proven, not speculative: Nanite runs `go-workflow` in production
  for Agent Workflows today.

### Negative

- `go-workflow` is pre-v1 (`STABILITY.md`): an exact tag must be pinned, no
  floating "latest," and any `v0.2` bump is a deliberate, evidenced
  migration.
- Implementing and qualifying a real durable host (`StateStore`, scheduler,
  wait endpoints) is real integration work, not an import-and-go swap, and
  must pass go-workflow's own conformance suites to count as done.
- Migrating every existing `pipelines:` definition and retiring
  `internal/pipeline` in one pass is a one-time migration cost with no
  parallel-running fallback window — the explicit trade for not carrying two
  pipeline engines.
- Another SQLite schema/store to operate alongside the existing domain
  store, at least until Postgres support lands.
- AWS resource management, DNS management, and GUI/frontend work are
  explicitly out of scope for this decision and unresolved by it.

## Follow-up direction

1. ~~Inventory current `pipelines:` definitions and `internal/pipeline` call
   sites (`internal/connector/local/connector.go`, `internal/webui/*`) before
   scoping the migration into concrete tasks.~~ **Done 2026-09-15** — see
   Tesseract `project/cerberus/knowledge/investigations`, key
   `pipeline_engine_inventory_2026_09_15`. Findings: config surface is a
   single file (`cerberus.cerberus.yaml`, 2 pipeline definitions); dead
   pipeline-run storage code and the vestigial `Pipeline` resource type were
   deleted in the same pass (`go build`/`make test` verified green).
2. Extend the Docker connector with `build`/`push`/`create`; wire registry
   credentials through `connector-secrets.yaml`.
3. Stand up the `go-sqlite`-backed durable store for `go-workflow` and
   `go-scheduler` state; evaluate `go-queue` as the scheduler's `Runner` seam.
4. Wire `go-workflow` host bindings: `StateStore`, `ActivationScheduler` via
   `go-scheduler`, wait `Materializer`/`ResponderAuthorizer` on the existing
   HTTP API, and the frozen MVP step-kind registry (`cmd`, `http`, `mcp`,
   `gate`, `wait`).
5. Migrate existing `pipelines:` definitions to go-workflow graph sources;
   retire `internal/pipeline`, `cmd_pipeline.go`, and `tools_pipeline.go` in
   the same pass.
6. Track Cerberus's official-Go-MCP-SDK / Unix-socket transport work and
   reassess where wait/callback endpoints terminate once that lands.

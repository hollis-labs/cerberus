---
id: "CERB-CAP-711"
class: "capability"
name: "Mutation concurrency and the source-tree build lock"
summary: "Serializes resource mutations two ways: one coarse mutex inside the daemon covering every lifecycle verb, and a fail-fast kernel flock on the git toplevel covering the build phase across processes."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.93
confidence_label: "Both locks read in full and every caller enumerated; the contention error reproduced through redact.Text; no two-process race was actually staged, which would have needed a live mutation"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/buildlock/buildlock.go, internal/connector/local/build_lock.go, internal/cerbapi/resource_runtime_service.go:50"
tags:
  - "cerberus"
  - "class:capability"
  - "concurrency"
  - "locking"
  - "flock"
  - "build"
  - "supervision"
  - "agents"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "opMu lives on the supervision lane's runtime service"
  - type: "relates_to"
    target: "CERB-CAP-103"
    note: "the lock is held through build, install and activation of the artifact join"
  - type: "relates_to"
    target: "CERB-CAP-712"
    note: "the pipeline build and deploy actions take the same lock"
  - type: "relates_to"
    target: "CERB-CAP-710"
    note: "an in-process fallback runs outside opMu entirely"
  - type: "relates_to"
    target: "CERB-CAP-104"
    note: "the monitor takes opMu before acting on a restart decision"
  - type: "relates_to"
    target: "CERB-GAP-731"
    note: "nothing serializes apply, sync, reload, stop or remove across processes"
  - type: "relates_to"
    target: "CERB-GAP-537"
    note: "the config file with no backup also has no writer lock"
  - type: "relates_to"
    target: "CERB-DEC-165"
    note: "installing to a fresh inode and renaming is the other half of concurrent-safety"
---

# Mutation concurrency and the source-tree build lock

Two operators, or two agents, or an agent and the daemon's own monitor, can all
reach the same resource at the same time. Cerberus has two locks for that and
they cover different things.

**Inside the daemon: one coarse mutex.** `ResourceRuntimeService` holds
`opMu sync.Mutex` for the entire duration of `ReloadResource`, `StopResource`,
`DeployResource`, `ApplyResource`, `SyncResource` and `RemoveResource`, and
`resource_monitor.go` takes the same mutex before acting on a restart decision.
It is not per-resource and it is not a read-write lock: every mutation served
by one daemon process queues behind every other. Two agents calling
`cerberus_resource_deploy` concurrently therefore serialize rather than race,
and the monitor cannot restart a resource in the middle of somebody's deploy.
The cost is that a slow deploy blocks an unrelated reload, which is a
throughput choice, not a correctness one.

**Across processes: an flock on the source tree.** `internal/buildlock` is 70
lines and every line is load-bearing. `Acquire` opens
`<root>/.cerberus-build.lock`, takes `LOCK_EX|LOCK_NB`, and on contention
decodes the holder record to produce

    build in progress for source tree <root> (resource "<id>", PID <n>,
    age <d>); retry when it finishes

It fails fast rather than waiting, so a second caller gets an answer instead of
a hang. It never unlinks the lock file, because removing it would let another
caller lock a different inode while a holder is still active. The kernel
releases the flock when the process exits, crashes included, so there is no
stale-lock recovery path to get wrong. The holder metadata is explicitly
diagnostic only — the comment notes it can be briefly incomplete while the
holder writes it, and the kernel lock is authoritative either way.

`WithBuildLock` in the local connector decides the lock's scope. It resolves
the build root from the strategy's `source.root`, then asks
`git rev-parse --show-toplevel` and locks *that*, so two resources built from
different subdirectories of one monorepo share a single lock. It is re-entrant
through a context key, so a deploy that internally calls the standalone build
path does not deadlock against itself. And it is taken by exactly three
callers: `DeployResource`, the pipeline's `build` and `deploy` actions, and
`BuildProcessResultContext`. Held, per its own doc comment, through build,
install and activation.

The gap between those two locks is the whole of the concurrency story that is
not covered. `WithBuildLock` returns a no-op when the spec has no build
strategy, and `apply`, `sync`, `reload`, `stop` and `remove` never ask for it
at all — so cross-process serialization exists only for the build phase of a
resource that has a build (CERB-GAP-731). `~/.cerberus/config.yaml` and the
registry index have no writer lock of any kind: a grep for `Flock`, `O_EXCL` or
a mutex across `internal/registry` and `internal/configops` returns nothing.

One control that used to exist no longer does. `internal/pausectl` still
exports `PauseAll` and `ResumeAll`, writing and removing `~/.cerberus/paused`,
and nothing calls either — `cmd_pause.go` was deleted in the v2 runtime
cut-over. What remains reachable is per-service pause, which `StopResource`
sets and `apply`/`deploy`/`reload` clear, surfaced as `OperatorStopped`. So
there is no way to quiesce the monitor across the estate for a maintenance
window; you stop resources one at a time.

## What it owns

- `opMu`: full serialization of every resource mutation within one daemon process
- the monitor taking the same mutex, so auto-restart cannot interleave with an operator action
- `buildlock`: a fail-fast, crash-safe, kernel-enforced flock on the git toplevel
- an error that names the holding resource, PID and lock age rather than just refusing
- re-entrancy, so nested build calls reuse one lock
- monorepo scoping: one lock per repository, not per build directory

## What it does not own

- any lock on apply, sync, reload, stop or remove (CERB-GAP-731)
- any lock for a resource with no build strategy
- writer locking for `config.yaml` or the registry index
- a global pause: `PauseAll` has no caller
- per-resource granularity — `opMu` is one lock for the whole service

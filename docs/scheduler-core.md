# Programmatic scheduler core

CW-20261008-0115 adds an inactive core in `internal/scheduling`, built on
`github.com/hollis-labs/libs/util/scheduler` and its `sqlstore` at **util/v0.4.0**
(`da18d11cb2df5206120c3c1524067bfd17153b72`). 0116 binds inactive CRUD/history surfaces; see `scheduling.md` for the current
CLI/MCP/HTTP contract and 0117 supported pipeline delivery limits.

`scheduling.New(ctx, db, executor, authorizer, options)` borrows a dedicated,
application-owned SQLite database. Its host chooses the database path under its
data directory, file permissions and connection pool/driver settings. The shared
store applies WAL, busy timeout and its migrations; the core adds an effect-receipt
table. Construction, migration, inspection and job creation start no timer or
effect. A host must explicitly call `Start` or `TickNow`, and must call `Stop`
to drain admitted work before closing the database. Tests use their own database
files and fake clocks, executors and permits.

## Jobs and history

A job has an app namespace (`OwnerApp`), ID, display name, exactly one cron,
interval or one-off schedule, portable IANA location, explicit target, enabled
flag, overlap/misfire/retention settings and a positive per-fire timeout. IDs and
reference names are bounded names; the namespace and ID form the library schedule
key. A content hash identifies the immutable job revision. An empty location
means UTC. A zero enabled flag disables dispatch. Supported targets are:

- `resource_start`: the existing v2 local/process `ApplyResource` operation;
- `resource_deploy`: `DeployResource`;
- `pipeline_run`: `RunPipeline`, including its existing whole-pipeline plan.

There are no command, agent-boot, legacy-service or provider targets in this core.
Environment references carry only environment and reference **names**. Jobs with
them require an explicitly bound resolver and the existing gated shell-only
pipeline transport added by 0117. Resource and mixed-action delivery remain
unavailable; references are never silently ignored. See `scheduling.md` for the
binding, capture and redaction limits.

`Create`, `Get`, `List` (by app), `Delete`, `Fire`, `History`, `Receipt` and
`Prune` are programmatic operations. Editing and pause/resume surfaces belong to
0116. Deleting a job preserves history/receipts and refuses its pending fires.
The library disables a one-off schedule after materialization; its job payload's
enabled flag still permits that one durable fire. This distinction prevents
confusing internal one-off completion with a disabled authored job.

The shared engine implements cron/timezones, intervals, bounded concurrency,
overlap, misfires, claims and terminal fire history. Every fire gets one attempt;
there is no mutation retry on a delivery error. `FireSucceeded` describes the
synchronous dispatch result, not the lifetime of a resource started by it.
The adapter decodes a pipeline's execution status: a successfully delivered DTO
containing failed execution is a failed dispatch, not success.

## Authority and brakes

**Unattended effects refuse by default.** A nil executor or authorizer refuses.
No production authorizer implementation or activation is supplied. An authorizer
is a trusted host dependency, never a policy, principal, ack or approval supplied
in a job. Injected test permits are not production authority.

At admission it must consult current authorization/revocation state and return
an ephemeral permit bound to the exact job key, revision, fire ID, target and
current runtime plan hash, with an expiry. Missing, revoked, expired or changed
bindings refuse. A pending/deleted/revised job or a stale dispatch claim refuses.
Jobs and history never persist a reusable `ack=true`, approval ID or permit.

`cerbapi.ScheduledExecutor` delegates to the existing shared runtime methods.
It replaces inherited caller claims with a distinct scheduler automation surface
acting for an agent, never the exempt monitor or a human. The ordinary intent
audit, acknowledgment, self-mutation guards, approval broker, rate controls,
brakes and redaction remain in effect. The ephemeral acknowledgment licenses
only that runtime call; the exact-fire permit and policy are additional gates.
An approval-required policy still refuses without the existing broker's proof;
the core does not invent unattended approval or weaken it to an ack.

Admission occurs under the resource/build/pipeline lock, using the checked
snapshot. Definitions, referenced pipeline resources, deploy source and the
plan are checked again before delivery. Lockdown, freezes and policy are re-read
after authorization. Scheduler policy is enforced even when the host's ordinary
enforcement is in shadow. The audit principal links the fire and job; the outcome
records the checked plan hash. Resolved-value redaction follows the request into
the runtime and renders errors before they enter library history or receipts.

## Delivery and recovery

A write-ahead receipt reserves a fire before its executor receives it. A fresh
permit and runtime recheck mark it `sent` before any effect. Receipts have these
states:

| State | Meaning |
| --- | --- |
| `prepared` | Reserved; no confirmed admission. A crash here requires reconciliation. |
| `sent` | Admitted; no terminal result recorded. A crash here has unknown outcome. |
| `completed` | Executor returned success after admission. |
| `failed` | Refused before dispatch, or execution returned a confirmed failure. |
| `unknown` | Post-admission deadline, transport/error ambiguity, or cancellation. |

A receipt surviving a restart prevents redelivery even if the shared engine
recovers an expired claim. The fire can become `skipped` (`duplicate_dispatch`),
but its receipt remains uncertain rather than becoming successful. Opening a
store neither rewrites uncertainty nor recovers by running an effect. Operators
must reconcile with the target before deciding on a **new** explicitly authorized
fire; there is no receipt-reset/retry API here. Pruning uses the library's durable
high-water fences and keeps effect receipts, so history cleanup cannot license
replay. Receipt retention/reconciliation tooling is a future operational concern.

Timeouts are cooperative contexts, bounded by the engine deadline so its lease
covers dispatch. This core does not promise to kill detached resources or undo
partial deployments. Cancellation/process termination remain owned by the
application runtime. `Stop` drains work and therefore needs cooperating executors.

## Platforms and scope

The core owns no process spawning, signal handling, native service paths or OS
locks. The runtime adapter delegates those to the existing local connector's
backends: launchd on macOS and systemd user units on Linux. Existing Unix process
group handling remains there. The core/executor boundary allows a later Windows
backend without adding Unix calls here; this change does not make the full
Cerberus application Windows-compatible.

Linux CI retains the complete build/vet/race gate. A focused Linux/macOS matrix
runs durable scheduling and shared-runtime admission tests with private roots and
fake targets. These are hosted source acceptance tests, not live-service acceptance.
0116 owns caller surfaces; 0117 owns env/secrets, notifications and log expansion;
0118 owns embedding; Windows connectors/install remain later work.

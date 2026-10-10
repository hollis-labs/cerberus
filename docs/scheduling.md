# Scheduled jobs

`cerberus schedule`, `cerberus_schedule_*` MCP tools and the HTTP API use
one Go service and the same job/result DTOs. The daemon owns the application
SQLite database resolved by go-apppaths (including its existing database
settings). CLI requests go through the daemon socket; a failed request never
falls back to a second database or replays a mutation. CLI `--config` and
`--db` overrides are rejected for this command: set the serving daemon's
configuration instead.

Normal daemon construction opens an **inactive** scheduling service. It does
not start an engine, attach an executor or grant per-fire authority. CRUD,
readback, history and previews are available through existing authenticated
surfaces. `run-now` currently refuses in production. Enabled jobs do not imply
permission and will not begin executing just because they were created.

## Jobs and selectors

A job file uses the shared `scheduling.Job` JSON schema, for example:

```json
{
  "id": "maintenance",
  "name": "Daily maintenance",
  "owner_app": "example-app",
  "timing": {"cron": "0 9 * * *", "location": "America/Chicago"},
  "target": {"kind": "resource_start", "id": "example-resource"},
  "enabled": true,
  "timeout": 10000000000,
  "overlap": "skip"
}
```

Choose exactly one timing: `cron`, positive `interval` in nanoseconds, or `at`
as an RFC3339 timestamp. Location is an IANA zone, default UTC. Durations
(`timeout`, `interval`, `misfire_grace`) are integer nanoseconds; timeout must
be positive and at most the core's fire deadline (30 seconds by default).
Supported targets are `resource_start`, `resource_deploy` and `pipeline_run`.
Commands, agent boots and legacy services are not targets in this version.
Environment references contain names only. A trusted host can bind aliases and
per-fire capture for shell-only pipelines as described below. Normal production
construction supplies no executor, authorizer, resolver or notification binding.

`owner_app` and `id` select a job; neither grants access. The serving host uses
its existing kernel-verified socket peer, verified OAuth identity/scopes, or
signed-in console session, then shared audit, lockdown, suspension and policy
gates. A missing serving caller binding refuses even a read. MCP client labels
and CLI human/agent classifications keep their existing meanings; job JSON
cannot supply them. All namespaces are managed by the serving operator's
existing permission policy; this is not an app isolation/embedding grant.

The service assigns `generation` and `incarnation`; omit both from submitted
job files. Readback contains `job`, `revision`, `state` and `next_run`.
States are `enabled`, `paused` and `completed` (a materialized one-off).
The job's enabled field is intent; completed is the durable recurrence state.

## CLI

```text
cerberus schedule create --file job.json --idempotency-key create-maintenance --ack --json
cerberus schedule get --app example-app --id maintenance --json
cerberus schedule list --app example-app --state enabled --json
cerberus schedule dry-run --file job.json --limit 5 --after 2026-10-10T00:00:00Z --json
cerberus schedule update --app example-app --id maintenance --file edited-job.json --revision REVISION --ack --json
cerberus schedule pause --app example-app --id maintenance --revision REVISION --ack --json
cerberus schedule resume --app example-app --id maintenance --revision REVISION --ack --json
cerberus schedule delete --app example-app --id maintenance --revision REVISION --ack --json
cerberus schedule run-now --app example-app --id maintenance --request-id run-once --ack --json
cerberus schedule history --app example-app --id maintenance --limit 100 --json
cerberus schedule logs --app example-app --id maintenance --fire-id FIRE_ID --json
```

`REVISION` is the exact revision from the last read; refresh it after each
edit. Updates replace the job definition and cannot move its app or ID.
Pause preserves its cursor; resume computes recurrence from now. Edits change
the generation so an old pending/claimed fire refuses before admission.
A dispatch already admitted cannot be undone by editing or deleting its job.
One-off resume preserves its authored one-off time; prior occurrence IDs and
prune fences still prevent replay of the same occurrence.

`--file -` (the default) reads standard input. File paths are opened with Go's
rooted file API; no shell, quoting expansion or external process is needed.
`--json` emits the shared result; operation errors include the stable code.
An enforced policy may request the existing approval flow; `--approval-id`
passes that approval, never a per-fire execution permit. `--ack` acknowledges
a metadata mutation; it is not permission to run a target unattended.

Create requires a names-only idempotency key, scoped to the app. Reusing it
with identical create content reads the current job in the original
incarnation. Different content refuses with `conflict`; deleting and
recreating the same selector does not resurrect the old create key.
Idempotency records, generation counters and effect receipts survive delete.
They are deliberately not garbage-collected by history pruning.

## MCP

The tools are `cerberus_schedule_create`, `update`, `delete`, `get`, `list`,
`run_now`, `pause`, `resume`, `history`, `logs` and `dry_run` (each prefixed
`cerberus_schedule_`). They are included in the canonical built-in tool list,
so stdio and MCP HTTP expose the same tools. Their input schema derives the
job shape from the Go DTO. Supply `job`, `owner_app`, `id`, `revision`,
`idempotency_key`, `request_id`, `state`, `fire_id`, `limit`, `after`, and
`acknowledged` as applicable. `approval_id` follows the existing MCP approval
contract. Results use the same JSON values as CLI and HTTP. Refusals are MCP
errors (`isError: true`) with a structured `error` containing code/message.
An embedded MCP host without a verified caller binding explicitly refuses;
standalone `cerberus mcp` forwards through the authenticated daemon socket.

## HTTP

POST a shared request to `/schedules/v1/OPERATION` on the daemon's existing
user-only socket, or `/api/schedules/v1/OPERATION` on the console. The console
requires its signed-in session, JSON content type, action token and allowed
Host/Origin for these POST requests, including read operations. MCP HTTP
uses its normal verified token forwarding and scope checks. No new listener
or authentication bypass is added. `/schedules/v1/call` is the socket
client's fixed transport route: its body must name `operation`.

For example the `get` body is:

```json
{"owner_app":"example-app","id":"maintenance"}
```

Create takes `job`, `idempotency_key` and `acknowledged: true`. Edits require
`revision`. `history` requires a limit of 1..1000; `dry_run` requires a job
and limit of 1..100. Unknown fields, extra JSON documents and mismatched
route/body operations refuse. Responses contain `result` or `error` plus
`rendered: true`. Error codes include `invalid` (400), `not_found` (404),
`conflict` (409), `unavailable` (503), and shared authorization/brake/policy
codes (403). Authentication middleware keeps its existing HTTP error shape.
These versioned routes are distinct from older daemon operations; an older
daemon cannot silently interpret a scheduling request as a plain mutation.

## Runs, logs and limits

A manual request selects one job and one durable request ID. It does not
call `TickNow` or dispatch other due jobs. Tests can inject an executor and a
trusted authorizer; the authorizer must provide a current exact-fire,
job-revision, target, plan-hash and expiry binding. The ordinary runtime
brakes/policy/admission and claim fences remain mandatory. Manual and engine
execution share the core's concurrency cap. Repeating a manual request ID
reads its existing run and never redelivers an effect; retained request IDs
still refuse replay after history pruning.

History contains the library's durable fire plus the effect receipt, when
present. A completed receipt means dispatch completed, not that a launched
resource later finished. `prepared`/`sent` after a crash and `unknown` after
ambiguous delivery need explicit reconciliation; none licenses a retry.
Timeouts and cancellation remain cooperative, not forced process termination.

`logs` uses the same `read_sensitive` serving permission/audit path and verifies
both fire ownership and job incarnation. A deleted/recreated job cannot read an
old incarnation's streams. A pruned run returns `not_found`. Captured runs return
`logs_available`, `logs.stdout`, `logs.stderr` and `logs.truncated`; unsupported,
unbound or capture-failed runs return an explicit availability reason. No
resource lifetime log or dispatch error is substituted for a captured stream.

## Bound pipeline delivery

A job can request `capture_logs: true` and `env_refs`, for example
`[{"env":"RUN_VALUE","ref":"service-token"}]`. These fields store only names,
never values, provider paths, caller identity or permits. The host constructs
`NewBoundSecrets(reader, bindings)` with an immutable copy of alias mappings
restricted to an exact target, env name and existing Cerberus secret service/key.
The reader is the existing read-only secret contract; the normal app installs
no such binding. Values rotate through that reader at run time without changing
the permitted alias mapping. Job/app selectors cannot choose a provider.

`ScheduledExecutor` extends the existing audited `RunPipeline` path. Its frozen
pipeline must contain only shell actions when per-fire delivery is requested;
mixed actions and resource start/deploy refuse before provider access or effects.
A shell action accepts either its existing `command` string (requires `sh`) or
portable `argv: [program, argument, ...]`, exclusively. Argv is cloned and included
in the current plan. No job-supplied arbitrary command target is added.
Caller JSON cannot carry argv at the request, job or target level: the shared
HTTP and MCP decoders reject unknown fields before calling the service. Human
and agent caller rejection fixtures cover these injection attempts. The source
G204 annotation covers only the intentional frozen operator-config argv edge;
it grants no execution authority and changes none of the runtime gates.

After exact-fire authorization and current policy checks, secrets resolve and
register in the same request redaction scope. Policy, plan, claim, current payload
and permit expiry are checked again before the existing sent CAS. Unsafe env
control variables, empty/reference-valued credentials, values below the scope's
8-byte protection minimum and oversized values refuse. Values never enter
command/args, history, receipts, results or notification payloads. The child-only
environment is private and never serialized. Existing process environment is
inherited; unrelated ambient values that Cerberus did not resolve are protected
only by its existing heuristic rules.

Shell stdout/stderr use separate sanitized writers before storage. Protected raw,
URL-escaped and JSON-escaped forms can span writes; bounded in-memory carry keeps
partial secrets out of persisted chunks. Streams retain rolling tails (64 KiB
per stream by default), rotating oldest sanitized bytes. `truncated` reports
rotation/global-cap drops; the default total retained body cap is 4 MiB, including
inaccessible orphan bytes, with at most 2,048 stream rows. Smaller host limits
are supported. Capture failure drains child pipes, marks logs unavailable and
does not retry execution. Errors, all progress fields/tokens, results and pipeline
slog messages use the same scope before emission. Build/deploy logs cannot
receive these job values because those mixed actions are refused.

A pipeline child exit failure is confirmed failure. Context cancellation,
transport ambiguity and inherited-pipe drain timeout remain uncertain; they do
not license retry or a confirmed failure notification. `WaitDelay` bounds waiting
on inherited pipes; it does not guarantee descendants stopped. Stop/pause/delete
retain existing cooperative lifetime guarantees.

## Failure episodes and delivery limits

Application completion facts commit with effect receipts and retain claim epoch
and incarnation. Reconciliation serializes chronological facts into one failure
alert per episode and one recovery notice after a confirmed successful dispatch.
Repeated committed misfire observations (two consecutive observations) open the
same episode; later failures are throttled until recovery. Unknown/prepared/sent
outcomes never become confirmed failures or recovery. Resource dispatch success
still means acceptance, not later process completion. Misfire callbacks are
passive: the library has no exhaustive durable callback journal, so crash-complete
misfire observation delivery is not promised.

Notifications contain fixed safe facts/IDs, not raw errors or job payloads. A
trusted host supplies `NotificationSink`; there is no mailbox/webhook
binding in normal production construction. Missing bindings record `unavailable`
outbox entries. Explicit engine/manual-run callbacks flush a bounded batch;
construction, reads and pruning never send notifications. Durable outbox states
are pending, reserved, delivered, unavailable and uncertain. A binding declaring
receiver-side durable EventID idempotency can retry the same ID after ambiguity.
Otherwise a reserved/uncertain event is never automatically resent: explicit
operator reconciliation is required. These source protocols and injected private
fixtures do not establish real notification-provider acceptance.

## Retention

`Core.Prune` reconciles durable completion facts, invokes the public library
`Store.Prune` with its terminal/KeepLastN/high-water rules, then deletes orphan
stream rows in a bounded transaction. There is a crash window between prune and
cleanup; orphans are inaccessible on every read/append, count against the global
cap, and are reclaimed on reopen or subsequent explicit maintenance. A late
writer cannot recreate a pruned fire. This is not an atomic prune/log transaction.
SQLite may retain freed pages/WAL bytes; logical retained-output limits do not
promise an absolute physical database-file size. Receipts, safe completion facts,
episode dedup and outbox obligations remain durable across prune/restart.

Acceptance uses owned SQLite/audit roots, fake targets and loopback clients
on macOS and Linux. Paths and output have no shell dependency. Native Windows
socket/runtime support remains a separate phase; no whole-Windows support is
claimed.

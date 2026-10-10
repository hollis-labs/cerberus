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
Environment references contain names only; execution with them refuses until
the separate environment-delivery contract is implemented.

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

`logs` verifies the run belongs to the selected job, then explicitly returns
`logs_available: false` and a reason. It does not synthesize output from job
metadata or mistake history errors for process logs. Per-run log delivery,
notifications and environment/secrets delivery belong to the separate follow-up.

Acceptance uses owned SQLite/audit roots, fake targets and loopback clients
on macOS and Linux. Paths and output have no shell dependency. Native Windows
socket/runtime support remains a separate phase; no whole-Windows support is
claimed.

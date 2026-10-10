# Choose where an app schedules work

Embed the published scheduler when activation and authoritative state belong in
an app's own transaction boundary. Use Cerberus for supported system operations
that need its resource or pipeline runtime. Sharing the library avoids private
implementations of recurrence, claim leases and fire identity; it does not require
every app to use the Cerberus daemon.

## Decision table

| Requirement | Owner | Reason |
| --- | --- | --- |
| Ingest, queue activation or other app-internal work tied to the app database | Embed `libs/util/scheduler` | The app retains job types, persistence, readiness and execution policy. |
| Tight activation latency with app-local durable state | Embed | Choose the app's tick cadence and clock without an extra process boundary. |
| Start or deploy a registered resource, or run a registered pipeline | Cerberus | Existing runtime plans, audit, approval, locks, brakes and secrets remain authoritative. |
| Scheduling metadata must survive a consumer being offline | Cerberus, when the target is supported | The daemon can own durable scheduling independently; production activation still needs a separately approved host binding. |
| App callbacks, arbitrary commands or agent boots | Design a supported target first | These are not Cerberus scheduling targets. Do not encode commands into selectors. |
| App readiness, dependency, budget or concurrency evaluation | The app | A timed fire is a trigger and must enter ordinary app admission. |
| Secret delivery or unattended approval | Explicit trusted host authority | A job definition, enabled flag, namespace grant or `--ack` cannot authorize an effect. |

Cerberus currently supports `resource_start`, `resource_deploy` and
`pipeline_run`. Its normal scheduler is inactive, with no executor, exact-fire
authorizer, app grants, secret resolver or notification binding. Creating an
app-scoped job in a private host fixture does not activate the daemon. Resource
and mixed-action per-fire environment/capture remain unavailable; supported
shell-only pipeline delivery and bounded logs require explicit trusted bindings.
Real notification delivery and crash-complete misfire callbacks remain incomplete.
See [the serving contract](scheduling.md) and [effect admission](scheduler-core.md).

## App namespace contract

The host constructs `cerbapi.ScheduleGrants` from copied, operator-owned grants.
A grant binds verified OAuth **issuer and subject**, optionally an exact token ID,
to one app. It also supplies a job quota from 1 through 128. The service accepts
only a private request proof minted by the existing daemon bearer-authentication
path; exported principal fields, client labels, console sessions and kernel UID
alone cannot mint this proof. Apps sharing a UID therefore cannot select each
other's namespaces merely by changing `owner_app`.

The service re-verifies the actual token against current authentication,
revocation, scopes and expiry, and checks the grant epoch after waits, before
writes and commits, and before returning results. Replacing a grant snapshot
invalidates requests still in flight, even if the replacement contents match.
A refusal before commit rolls back the metadata transaction. A revocation after
commit can refuse disclosure while leaving the committed mutation intact; use
the original idempotency key to reconcile after authorization is restored.
This is not a promise to undo an already committed mutation.

For a manual fire, the final claim snapshot is read before the last token/grant
verification. Only local context, claim-lease and permit-expiry checks follow
before admission returns. The original send CAS can already be durable when a
final check refuses: an `unknown` or unresolved `sent` receipt still prevents
replay. Expired claims cannot write authoritative completion facts. These checks
cannot make revocation atomic with an external effect after admission or retract
an effect that was already admitted.

An omitted app selector resolves to the caller's bound app. A mismatched selector,
job owner or registration entry refuses before reading or writing another app.
List, get, edits, history, logs, previews and registration share this boundary.
The key remains `owner_app/id`; two apps can both register `maintenance`.
App and ID names match `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`. Display names are
nonempty and at most 256 bytes. Project subdivision is not a separate authority
boundary in this contract; apps can use distinct IDs inside their namespace.

`admin_view` is a separate, acknowledged, audited `admin` operation requiring an
explicit host `AdminView` grant and the ordinary operate scope and policy gates.
It permits a cross-app list, optionally filtered by app/state. It grants no
cross-app edit, delete, history or log access. An ordinary operate token is not
an admin grant. Normal source construction provisions no grants; app operations
refuse until a trusted host supplies them. Production grant enrollment, credential
provisioning and activation are outside this change. The existing MCP HTTP
client forwards bearer credentials to the daemon; the normal CLI, MCP stdio
and console clients provide labels or sessions instead. Those clients still
refuse app operations: this change supplies no token-file/env credential loader
or replacement proof for them. Their shared DTO adapters are tested separately
from real signed-token HTTP/socket namespace acceptance.

## Declarative registration

All surfaces expose `register`: CLI `schedule register`, MCP
`cerberus_schedule_register`, HTTP `POST /schedules/v1/register` (or its versioned
`call` envelope). CLI reads a JSON array from `--file` or stdin; API/MCP use a
`registration` array inside the shared call. A batch has 1 through 32 unique jobs
from one app, a names-only `idempotency_key`, and ordinary mutation acknowledgment.
HTTP and CLI input remain bounded to 1 MiB. Quotas count all stored jobs, including
paused and completed ones, and are enforced atomically with each batch/create.
Deleting a job frees that active-job slot but preserves receipt and incarnation
fences. Durable idempotency records have no garbage-collection policy here.

```json
{
  "operation": "register",
  "owner_app": "example-app",
  "idempotency_key": "startup-v1",
  "acknowledged": true,
  "registration": [{
    "absent": true,
    "job": {
      "owner_app": "example-app",
      "id": "maintenance",
      "name": "Daily maintenance",
      "timing": {"cron": "0 9 * * *", "location": "UTC"},
      "target": {"kind": "pipeline_run", "id": "maintenance"},
      "enabled": true,
      "timeout": 10000000000
    }
  }]
}
```

Each entry supplies exactly one precondition: `absent: true` for its first create,
or `revision` from the latest read for an explicit definition change. An existing
registration of the **same desired definition in the same incarnation** is a
no-op even under a fresh startup key: user edits, pause, completed state, current
revision, due cursor and incarnation stay intact. It is not continuous
reconciliation that overwrites operator decisions. An existing job without a
matching desired record needs its current explicit revision; the service cannot
infer ownership from a matching name.

Change the desired definition with a new batch key and the current revision.
Changed content under an old key refuses. Changes retain incarnation and pause;
an already completed job remains completed. Ordinary manual resume remains a
separate operation. Omitted jobs are never deleted. A stale revision or quota
failure rolls back the entire batch. Replaying an old request after a deletion
or recreation refuses rather than resurrecting its original incarnation.
Registration creates no fire and sends no effect. Updates keep the existing
current-payload/claim/permit, plan/send-CAS, shared concurrency and uncertain-no-
replay fences.

## Upgrade fragments ingest scheduling

CW-20260930-0056 identifies fragments-engine's pre-CAS v0.1.0 Store and the loss
window between advancing `next_run` and completing ingest. Keep ingest activation
embedded: its ingest state and execution policy belong to fragments-engine.
The current published implementation used by Cerberus is
`github.com/hollis-labs/libs/util/scheduler` and `scheduler/sqlstore` at util/v0.4.0.
A consumer adoption should verify its current tree and the selected published
version rather than assume the historical task report still describes it.

Replace `ListDueSchedules`/`ClaimAndUpdateScheduleRun`/`SetScheduleNextRun`/
`DisableSchedule` with the current `Store` contract. Materialize a durable fire
and advance recurrence in one CAS transaction. Derive its ID only from schedule
ID and scheduled UTC time. Persist attempts, claim epoch and lease expiry;
recovery keeps the fire ID and attempt while fencing the old claim owner. Either
adopt the published SQL reference schema in an app-owned SQLite database or
implement the same Store contract in the existing app schema. Do not leave an
uncoordinated old timer writing the same schedules during cutover.

Make the ingest Runner deduplicate by durable fire ID in the app's own ingest
admission transaction. Confirm restart recovery, simultaneous claims, bounded
retry/backoff, terminal history and prune fences using injected clocks and the
library conformance fixtures. Decide how old due occurrences enter the new store
before migration; do not silently treat advanced cursors as completed ingest.
This guide changes neither fragments-engine's code nor its live scheduler.

## Upgrade Torque timed activation

CW-20260904-0163 distinguishes Torque's readiness queue from recurring schedules.
Keep timed activation embedded with the app's authoritative task database. A fire
must enter the existing readiness, dependency, budget, gate and concurrency path.
It must not dispatch an agent directly or create a separate Cerberus-owned queue.

Prefer creating a task from a recurring template for each fire, rather than
re-arming a completed task with old run state. Record fire-ID-to-task identity
atomically with admission to the existing queue so a recovered claim cannot
create a second task. Implement schedules/fires and claim CAS in the current
SQLite adapter, with an equivalent Postgres transaction design where required;
the reference `sqlstore` is SQLite-specific. Retain stable IDs, leased recovery,
retry bounds and durable terminal history. A refusal of readiness is an app
activation outcome, not permission to bypass the queue. Verify crash recovery at
the fire/task/queue boundary with the existing adapter and library conformance
contracts before enabling it. This guide performs no Torque consumer adoption.

## Platform boundary

The namespace, registration and scheduler core use portable Go/SQL contracts.
Private signed-token/socket and scheduling fixtures run on macOS and Linux in the
existing scheduler CI matrix. Native Windows socket transport, connector execution
and installation remain later work; this guide does not claim Windows acceptance.
Keep process spawning, signals and service installation inside runtime backends,
not namespace or Store implementations.

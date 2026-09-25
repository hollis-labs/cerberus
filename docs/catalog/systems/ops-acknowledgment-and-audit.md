---
id: "CERB-CAP-604"
class: "capability"
name: "Operation acknowledgment and the audit log"
summary: "Every operation that is not a read \u2014 and any that writes the local filesystem \u2014 demands an explicit acknowledgment on every surface, checked before credentials or arguments resolve; a dry run previews or is refused; and every call on every lane, plus the monitor's restarts, is recorded as intent and outcome in an append-only hash-chained log that `cerberus audit` reads, verifies and prunes."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "gate and dry-run exercised live at audit time; P0 gate changes re-read on main after P0 (#48 to #54); log field inventory taken from the full 3050-line log; remedy recorded in WP-S1 on 2026-09-18; P1-4b runtime, monitor, telemetry and audit CLI tests read and run on the branch"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/external_connector_service.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "audit"
  - "acknowledgment"
  - "dry-run"
  - "provenance"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-DEC-822"
    note: "how the audit log is stored"
  - type: "relates_to"
    target: "CERB-GAP-856"
    note: "the resource lane, pipelines, profiles and audit CLI \u2014 closed in P1-4b"
  - type: "depends_on"
    target: "CERB-CAP-602"
    note: "the daemon log is the only candidate trail"
  - type: "relates_to"
    target: "CERB-GAP-630"
    note: "no audit trail — must-have"
  - type: "relates_to"
    target: "CERB-GAP-639"
    note: "no read-only exec lane below the ack gate"
  - type: "relates_to"
    target: "CERB-GAP-648"
    note: "LogAudit — deleted in P1-4b"
  - type: "relates_to"
    target: "CERB-GAP-838"
    note: "the gate is satisfied by the caller being gated"
  - type: "relates_to"
    target: "CERB-CAP-200"
  - type: "relates_to"
    target: "CERB-GAP-846"
    note: "an in-process caller is not classified as a principal"
  - type: "relates_to"
    target: "CERB-GAP-851"
    note: "the gate runs after credential resolution"
---

# Operation acknowledgment and the audit log

The gate works, and it is strict. `cerberus ssh exec <work-host> -- id -nG` with
no flags exits 1 with `acknowledgment_required: exec operation "exec"
requires operator acknowledgment` (the wording names the effect since PR #60). Adding `--dry-run` alone returns a clean
preview DTO naming the connector, operation, resolved target and the command
that would run, without executing it. That is the intended shape and it behaves
as documented.

The sections below up to "The planned remedy" describe the state before the
audit log existed, and why it was needed. What P1-4a built is at the end, under
"The audit log (P1-4a)".

P0 tightened the gate in three ways, all in PR #49. It fails closed: an
operation the connector does not declare, or a connector with no definition, is
refused as `operation_unsupported` rather than waved through. A dry run never
executes: an operation with no preview returns `preview_unsupported`. And a
plugin's destructive operation needs `--ack` whatever its manifest's
`requires_ack` says. Three writes that took no `--ack` now do:
`digitalocean create_droplet`, `digitalocean stop` and
`forge update_deployment_script`. None of that changes what this record is
about. The gate is still satisfied by the caller being gated (CERB-GAP-838), and
it still leaves no record.

What does not exist is any record of the acknowledgment. Cerberus logs socket
requests, and only socket requests: across the whole 3050-line log, every one of
the 2621 `daemon.socket.request` entries carries `time`, `level`, `msg`,
`method` and `path`, and nothing else. The path is genuinely informative —
`POST /connectors/digitalocean/operations/list_droplets`,
`POST /resources/<tunnel-resource>/apply`,
`POST /connectors/ssh/operations/exec` — so you can reconstruct which operation
was invoked and when. You cannot reconstruct anything else.

Put concretely, against the question this record exists to answer. If a droplet
were destroyed through Cerberus, the log would contain one line reading
`POST /connectors/digitalocean/operations/destroy_droplet` with a timestamp. It
would not contain the droplet id, because arguments are not logged. It would not
contain whether `--ack` was passed, because the flag is not logged. It would not
contain whether the call succeeded, because results are not logged. And it would
not contain who made it, because there is no caller identity in the record at
all — no uid, no pid, no session, no indication of surface. A `--ack` arriving
from an MCP tool call is byte-identical in the log to one typed by a human,
which is the specific problem worth naming: agents are first-class callers here,
and the acknowledgment gate treats an agent's acknowledgment as an operator's.

There is one vestige of an intent to do better. `internal/service/lifecycle_log.go`
exports `LogAudit(operation, serviceID, reason, taskID, sessionID string)`,
documented as capturing "who requested the operation and why, so operators can
trace service disruptions back to the responsible agent/task". It has no callers
anywhere in the repo, and it drops its first argument: `operation` is accepted
and never placed in the attribute list, so even a called `LogAudit` would record
a service, a reason and optional task and session ids without saying what was
done to it. The single `lifecycle.audit` line in the log, dated 2026-03-23
against a long-gone `engine-api`, shows exactly that shape.

The SQLite store is not an alternative answer. `cerberus path` resolves
`main-db` to
`~/.local/share/cerberus/workspaces/default/main.db`, and on the machine
audited that directory is empty: the database had never been created. There is
no persistent store for an audit trail to live in until something creates one.

## The planned remedy

`docs/plans/agent-authority-and-secrets.md` owns this as WP-S1. Three
decisions recorded there constrain any implementation of this capability:

The record is written at the **service layer**, not in HTTP middleware.
`ExternalConnectorService.Execute` is the single chokepoint for every connector
operation on every surface, whereas middleware sees method and path and misses
the in-process path `cmd_transport.go` hands the CLI — which is the path a
mutation takes when the daemon is unreachable.

**Caller identity has to be added to the socket wire**, which carries none
today, or every daemon-delivered operation stays anonymous and an
agent-driven destructive call remains indistinguishable from an operator's. The
header is self-reported by design: anything that can reach the socket can set
it, so it attributes between our own surfaces and is not an authentication.

**The record is a DTO** under ADR 0003. `ExternalConnectorOperationArgs.Config`
is an untyped map carrying whatever the caller passed, so serialising the
request wholesale would make the audit log the credential store nobody meant to
build, with longer retention than the real one. Fields are allow-listed and
credentials appear as names only.

## Since P1 (PRs #60 and #63, and the P1-3 branch)

**The gate follows the effect, not a flag** (CERB-DEC-816). Every operation's
`requires_ack` is derived from its contract (CERB-CAP-212). It is true for
`write`, `lifecycle`, `destructive`, `exec` and `admin`, and for any operation
with `local_fs: writes`. `read` and `read_sensitive` need none. Operations that
newly need `--ack`:

- **CLI:** `server start` and `stop`, `docker up`, `down` and `destroy`,
  `ssh get` and `get-dir`, `resource deploy`, `ensure-fresh`, `apply`, `reload`,
  `stop`, `sync` and `remove`, and `pipeline run`.
- **MCP:** `acknowledged` on the matching tools.
- **Web console:** a confirm step, the only thing that sends `acknowledged`.
- **Deploy profiles:** the console confirms a deployment-profile run against
  the commands it will execute.

**It runs before anything resolves.** Argument refusals (key table, required,
one-of, JSON type) and the acknowledgment check come before credential
resolution on the admin lane. The resource runtime gate comes before its locks
and the resource lookup (`runtimeGate`). An unacknowledged call never depends on
having a credential.

**Refusals keep their code on every surface.** `acknowledgment_required` is 409
on the socket and web, `isError` on MCP, and an error on the CLI. Uncoded
execution failures are `operation_failed` (502), so a 500 still means a fault in
Cerberus.

**Supervision is not gated.** The resource monitor's `auto_restart` and
health-driven restarts call the local connector directly and never meet the
gate. `TestMonitorRestartNeverHitsTheGate` pins it.

The acknowledgment is still supplied by the caller being gated
(CERB-GAP-838). What changed in P1-4a is that it is now recorded.

## The audit log (P1-4a)

**Where it is.** `~/.cerberus/audit/YYYY-MM.jsonl`, one file per calendar month,
mode 0600, in a 0700 directory (`internal/audit`, opened once per process by
`app.AuditSink`). A file is only ever appended to. Every record carries a
sequence number, the previous record's hash and its own hash, a SHA-256 over
the record's JSON with `prev_hash` inside it, so the chain runs across the
monthly files. The very first record in a directory is `chain_start`. Each new
month opens with `file_start`, naming the previous file. A torn last write from
a crash is left as it is, and the next write appends a `chain_break` and
resumes from the last complete record. `audit.Verify` walks every file in order
and reports any gap in the sequence, broken link, wrong hash or unexplained torn
line. Storage choices are recorded in CERB-DEC-822.

**One chain, several writers.** The daemon and an in-process CLI both write
here. Every append runs under an exclusive `flock` on the directory's `.lock`
and a per-process mutex, and re-reads the chain tail when another writer has
appended since. Every append is fsynced before the write returns.

**Two records per call.** An `intent` is written before anything runs — before
the contract gate, so a refusal is recorded too — and an `outcome` on every exit
with the same `operation_id`. Each record is a DTO, not a copy of the request:

- `principal`: the caller surface (`socket`, `web`, `in_process` or `unknown`),
  marked `self_reported`. An in-process CLI call is a local principal, never
  "the human".
- `connector`, `operation`, `effect`, and `target`: the contract's target kind
  and the values of its target fields only, such as a droplet id.
- `args_digest`: an HMAC-SHA256 of the arguments under a per-install
  `.digest_key`. No other argument value is recorded.
- `credential_names`: the connector's declared secret names, as
  `connector/name`, on outcomes that got past the gates. None on a refusal or a
  host-preview dry run.
- `acknowledged`, `dry_run`, `decision` (`allowed` or `refused`),
  `outcome_code` (`ok` or the error code), `duration_ms`, and `posture`
  (`secure`).
- For a plugin operation, `plugin_config_sha256` and
  `plugin_entrypoint_sha256`, so a record shows which config and which binary
  ran.

**An unwritable log is not silent (Decision 8).** When the intent cannot be
written, a non-read or unclassified operation is refused with
`audit_unavailable` (503, exempt from redaction) and nothing runs. A read goes
on, with an `audit.write_failed` error in the daemon log. A directory that
cannot be opened gives `audit.Unavailable`, which refuses every write the same
way. A failed outcome write is logged, and shows as an intent with no pair.

**What is recorded today.** The admin lane (`ExternalConnectorService.Execute`,
every exit), the managed plugin direct route, and plugin install, load, unload
and uninstall. The lifecycle calls are recorded as `admin`, connector
`plugin`. The one-shot `connectors plugin exec` and `health` are recorded too,
as unclassified and `admin` respectively. The admin lane's call into a plugin is
recorded once, not twice.

**What keeps it that way.** The sink is a required constructor argument of
every service that writes it, and a nil sink panics. Outside tests those
services are constructed only in `internal/app`
(`TestServicesAreConstructedOnlyInApp`). Every `cerbapi.Client` method is
classified as audited, read-only or pending, and a new method fails until it is
classified. Test packages that reach the real sink point `HOME` at a scratch
directory in `TestMain`.

## The rest of the trail (P1-4b)

**Everything that acts is recorded.** The resource mutators (`deploy`, `apply`,
`reload`, `stop`, `sync`, `remove`) and pipeline runs are wrapped in
`internal/cerbapi/runtime_audit.go`: an intent before the runtime gate, an
outcome after, and an `OpResult` or pipeline result reporting failure is
recorded as `operation_failed`. A pipeline run is one record, as it is one
acknowledgment. Decision 8 holds: an unwritable log refuses them as
`audit_unavailable` before the operation lock. A deploy-profile run
(`RunDeploymentProfile`) is recorded with its credential names, and the web
console is constructed with the sink. Every `cerbapi.Client` method is now
classified audited or read-only; none is pending.

**Automation is recorded, never gated.** The resource monitor's restarts
write an intent and an outcome with principal kind `automation`, surface
`monitor`, not self-reported, and a `reason` naming what it saw and the
attempt count. An unwritable log does not stop a restart.

**Plugins enrich, never write.** A plugin may return events under
`cerberus_telemetry` in its result, which the host strips before the caller
sees it, and the host tees the plugin's stderr to the calls in flight (lines
written while two calls overlap are marked `shared_stderr`). Both are bounded
— 32 events, 32 lines, 512 bytes a field, 8 KiB a record, with `truncated` set
past that — run through the plugin's own value redactor, and attached by
operation id to the host-written outcome as `plugin_telemetry`.

**Reading it.** `cerberus audit` reads `~/.cerberus/audit` directly and needs
no daemon. `tail` shows the latest records; `query` filters by connector,
operation, surface, outcome (code or decision), target id and a time range;
both print text or, with `-o json`, the records as written. `verify` checks the
chain across month files and exits non-zero on any break other than a
recorded `chain_break`. `prune --before <date>` is an admin operation: it runs
only from an interactive terminal, asks for a typed confirmation, removes only
whole month files and never the newest, and records its intent before removing
anything — an unwritable log removes nothing. `verify` accepts a chain whose
first file opens with `file_start` only when a recorded prune removed the file
it names.

`LogAudit` is deleted (CERB-GAP-648).

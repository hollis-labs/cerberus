---
id: "CERB-CAP-604"
class: "capability"
name: "Destructive-operation acknowledgment, and the absence of an audit trail"
summary: "Every destructive connector operation demands an explicit --ack, a dry run either previews or is refused without executing, and the gate fails closed on an undeclared operation, but nothing records who acknowledged what: the only trace is a method-and-path line with no caller, no arguments and no outcome."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "gate and dry-run exercised live at audit time; P0 gate changes re-read on main after P0 (#48 to #54); log field inventory taken from the full 3050-line log; remedy recorded in WP-S1 on 2026-09-18"
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
    note: "LogAudit drops its operation argument"
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

# Destructive-operation acknowledgment, and the absence of an audit trail

The gate works, and it is strict. `cerberus ssh exec <work-host> -- id -nG` with
no flags exits 1 with `acknowledgment_required: destructive operation "exec"
requires operator acknowledgment`. Adding `--dry-run` alone returns a clean
preview DTO naming the connector, operation, resolved target and the command
that would run, without executing it. That is the intended shape and it behaves
as documented.

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

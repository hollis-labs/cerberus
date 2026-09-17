---
id: "CERB-CAP-105"
class: "capability"
name: "Unsupervised resource reporting"
summary: "Reports a resource the supervision lane does not own as `unsupervised` with the connector commands that do operate it, instead of a blank status or a dead-end error."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.95
confidence_label: "verified live against muctlvaig (server/ssh) for both resource list and resource status"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/unsupervised.go"
tags:
  - "cerberus"
  - "class:capability"
  - "supervision"
  - "unsupervised"
  - "boundary"
  - "ux"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-100"
    note: "how the lane answers for kinds it does not own"
  - type: "implements"
    target: "CERB-DEC-162"
    note: "unsupervised is a word, not a blank cell"
  - type: "implements"
    target: "CERB-DEC-160"
    note: "the reporting path is what makes the deferral liveable"
  - type: "relates_to"
    target: "CERB-CAP-201"
    note: "named as the operating lane for server/ssh resources"
  - type: "relates_to"
    target: "CERB-CAP-202"
    note: "named as the operating lane for container/docker resources"
---

# Unsupervised resource reporting

`internal/cerbapi/unsupervised.go` exists because of a specific bad experience. A `server`/`ssh` resource like `muctlvaig` is a legitimate, working declaration — a named handle for connector operations — but the supervision lane has no runtime state for it. The old behaviour was a blank STATUS cell, which is indistinguishable from a probe that failed, and an error message that said "status currently supports local process resources only": it told the operator what did not work and left them to guess what did.

The fix is small and deliberate. `UnsupervisedStatus` is the literal string `unsupervised`, chosen over an empty string because a word cannot be misread as a defect. `UnsupervisedReason` says the resource is administered through its connector rather than supervised by the local runtime lane. `UnsupervisedNextStep` names actual commands, switching on the connector: `docker` gets `cerberus docker up|down|logs <id>`, `ssh` gets `cerberus ssh status <id> | cerberus ssh exec <id> -- <command>`, and anything else falls back to `cerberus connectors describe <connector>`.

The read paths and the mutation paths diverge on purpose. `resource list` and `resource status` answer the question — `status: unsupervised`, plus the reason and the next step — because "what is the state of this server resource" is a reasonable thing to ask and has a real answer. `resource inspect`, `deploy`, `apply` and the rest refuse with `UnsupervisedOperationError`, which still carries the reason and the same next-step text, because there is nothing for them to do.

Verified live on this machine:

```
$ cerberus resource status muctlvaig
Status:      unsupervised
Context:     server/ssh resources are administered through the ssh connector, not supervised by the local runtime lane
Next Step:   cerberus ssh status muctlvaig | cerberus ssh exec muctlvaig -- <command>

$ cerberus resource inspect muctlvaig
Error: inspect does not apply to "muctlvaig": server/ssh resources are administered through the ssh connector, ...; try: cerberus ssh status muctlvaig | ...
```

The `SupervisedLocally` helper carries the boundary in one place and is checked in the CLI before a verb is dispatched as well as inside the runtime, so an unsupported verb fails with guidance rather than with a decode error.

## What it owns

- the `unsupervised` status literal and its reason text
- connector-specific next-step command suggestions
- the read-vs-mutate split: status/list answer, other verbs refuse with guidance
- `SupervisedLocally` as the single expression of the lane boundary

## What it does not own

- any actual operation on the resource — it only names the connector commands
- validating that the named commands will succeed (a docker or ssh resource may still be misconfigured)
- per-connector next steps beyond ssh and docker; everything else gets `connectors describe`
- the 16 other hardcoded local/process checks scattered outside this file

## Where it lives

- `internal/cerbapi/unsupervised.go`
- `internal/cerbapi/resource_runtime_service.go`
- `cmd/cerberus/cmd_resource.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource list; cerberus resource status <id>; any supervision verb on a non-local/process resource.

## Recorded reasoning

- `docs/plans/infra-admin-control-plane.md`

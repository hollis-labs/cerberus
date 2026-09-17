---
id: "CERB-CAP-712"
class: "capability"
name: "The pipeline lane"
summary: "Executes ordered, dependency-resolved stages of build, deploy, start, stop, health-wait and shell actions against local process resources — the third execution lane, with no acknowledgment gate and no pipeline declared on this machine."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "The whole package read end to end and every surface traced; zero pipelines are defined here so nothing in the lane has been executed"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/pipeline/, internal/cerbapi/pipeline_service.go, cmd/cerberus/cmd_pipeline.go"
tags:
  - "cerberus"
  - "class:capability"
  - "pipeline"
  - "orchestration"
  - "dag"
  - "execution"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "build, deploy, start and stop actions drive the supervision lane's connector directly"
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "it is a third lane beside supervision and admin, not a part of either"
  - type: "relates_to"
    target: "CERB-CAP-604"
    note: "it executes mutations without the acknowledgment gate that capability owns"
  - type: "relates_to"
    target: "CERB-CAP-711"
    note: "its build and deploy actions take the source-tree build lock but not opMu"
  - type: "relates_to"
    target: "CERB-CAP-106"
    note: "actions.Deploy calls ValidateMutation, so the self-guard reaches it"
  - type: "relates_to"
    target: "CERB-GAP-143"
    note: "health_wait is the only live reader of a declared health URL"
  - type: "relates_to"
    target: "CERB-GAP-631"
    note: "the richer prober one package away still has no caller"
  - type: "relates_to"
    target: "CERB-GAP-536"
    note: "action types and stage dependencies are not validated at author time"
  - type: "relates_to"
    target: "CERB-GAP-733"
    note: "no acknowledgment and no dry-run on anything it runs"
  - type: "relates_to"
    target: "CERB-GAP-734"
    note: "rollback is a no-op for the two actions that change anything"
  - type: "relates_to"
    target: "CERB-CAP-401"
    note: "GET /pipelines, GET /pipelines/{id} and POST /pipelines/{id}/run"
  - type: "relates_to"
    target: "CERB-CAP-402"
    note: "cerberus_pipeline_list and cerberus_pipeline_run"
---

# The pipeline lane

`AGENTS.md` opens its architecture section with "Two lanes, and they are not
interchangeable." There are three. `internal/pipeline` is a complete execution
lane with its own DAG, its own executor, its own action set and its own
surfaces on the CLI, the socket, the console and MCP — and it is named in that
document only in passing, as a thing the config can contain.

A pipeline is declared under `pipelines:` in the v2 config: an id, a name, and
ordered `stages`, each with `depends_on` and one or more `actions`. `Resolve`
turns that declaration into executable actions, `buildDAG` turns the stage
dependencies into levels with DFS cycle detection, and `Executor.Run` walks the
levels, running the stages within a level in parallel.

Six action types resolve:

- `build` / `build_app` — the local connector's build strategy for a resource
- `deploy` / `deploy_app` — build then install then activate
- `start`, `stop` — the local connector's `Apply` and `Stop`
- `health_wait` — HTTP GET the resource's `health_check.url` (or `health`)
  until 2xx, 2-second interval, 5-second per-attempt timeout, 30-second default
  budget overridable per action
- `shell` — `sh -c <command>` in an optional `dir`, via `CombinedOutput`

`health_wait` deserves the emphasis. It is the only code in Cerberus that
actually fetches a declared `health:` URL. The supervision lane never does
(CERB-GAP-143) and `internal/service/healthcheck.go`, which implements the
richer version with command checks and intervals, has no caller outside its own
tests (CERB-GAP-631). So the one live prober in the binary lives in the lane
nobody has a pipeline in.

The lane reaches the local connector directly rather than through
`ResourceRuntimeService`. That has two consequences and they pull in opposite
directions. It does take the source-tree build lock, so a pipeline build cannot
race a CLI deploy's build. It does not take `opMu`, so a pipeline's install and
activation can interleave with a daemon-side apply of the same resource. The
serving-daemon self-mutation guard does reach it: `actions.Deploy` calls
`ValidateMutation` through an interface assertion, and the guard is installed on
the connector itself, so a pipeline cannot deploy the daemon over itself.

What it has that the admin lane has is the destructive-operation vocabulary —
and that is the sharp edge. A pipeline run takes no `--ack`, offers no
`--dry-run`, and is not flagged destructive on any surface, while `shell` runs
arbitrary commands and `stop` stops production resources. `cerberus_pipeline_run`
puts that behind one MCP tool call whose only required argument is a pipeline id
(CERB-GAP-733).

Rollback is advertised and largely absent. On failure the executor calls
`Rollback` on completed stages in reverse order, logging rather than
propagating rollback errors. `Start` and `Stop` are genuine inverses of each
other. `Build`, `Deploy`, `Shell` and `HealthWait` all return `nil` — `Build`
says why in a comment, "builds are not rollbackable", and `Deploy` says nothing
at all. A three-stage pipeline that deploys in stage one and fails in stage
three reports `failed`, runs its rollback, and leaves the new artifact
installed and running (CERB-GAP-734).

Verified live on 2026-09-17: `cerberus pipeline list` returns "No pipelines
defined." The live config declares none, so nothing in this lane has ever run
on this machine. Author-time validation would not catch much if one were added
— registry validation checks neither action types nor stage dependencies
(CERB-GAP-536); the checks that exist are in `Resolve` and `Builder.Build` and
fire at run time.

Marked `partial`: the implementation is complete and coherent, and it is
entirely unexercised here, undocumented as a lane, and missing the
acknowledgment gate every comparable verb has.

## What it owns

- the `pipelines:` config shape: stages, `depends_on`, actions
- stage DAG construction, level ordering and cycle detection
- level-parallel execution with a first-error result
- six action types, including the only live health prober in the binary
- reverse-order rollback of completed stages, best-effort
- MCP progress notifications and socket NDJSON progress for a run
- surfaces: `pipeline list|show|run`, `GET|POST /pipelines…`, `/api/pipelines…`,
  `cerberus_pipeline_list`, `cerberus_pipeline_run`

## What it does not own

- acknowledgment or dry-run for anything it executes (CERB-GAP-733)
- rollback of a build or a deploy (CERB-GAP-734)
- `opMu`, so it does not serialize against daemon-side resource mutations
- author-time validation of action types or stage dependencies (CERB-GAP-536)
- any non-local resource: `build`, `deploy` and `health_wait` all require
  `type: process` / `connector: local`
- an MCP tool or console view for `pipeline show`

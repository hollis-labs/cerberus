# V2 Runtime Handoff

Date: 2026-04-28

## Current Direction

Cerberus is now intentionally v2-first:

- `resources:` is the only active local workload model
- `services:` has been retired from the active runtime surface
- the TUI has been removed from the default runtime workflow
- CLI, daemon/socket API, and MCP should all be thin clients over one shared runtime service

The agreed model is:

- `resource` = managed thing
- `type: process` = local workload kind
- `mode: dev_session | os_service` = runtime policy

## What Is Already Done

### Config / Live Runtime

- managed workloads have been migrated to `resources:`
- `services:` is effectively unused/frozen in the live config
- the Cerberus daemon itself is now a v2 resource
- Ollama is now Cerberus-owned as `ollama-service`
- multiple project backends/frontends were cut over to v2

### Runtime Architecture

- shared v2 runtime service exists in `internal/cerbapi/resource_runtime_service.go`
- daemon-backed resource monitor exists in `internal/cerbapi/resource_monitor.go`
- `dev_session` no longer depends on `ManagedService` in the local connector
- pipelines now resolve against v2 resources, not the legacy service slice
- daemon health now includes v2 resource health
- the legacy service runtime boundary has been removed from the active daemon/client path

### Public Surface

- root CLI help now groups commands into:
  - `V2 Resource Commands`
  - `Daemon And Runtime Commands`
  - `Platform And Connector Commands`
- root `cerberus` now shows help instead of launching the TUI
- legacy CLI lifecycle commands were removed from the active surface
- legacy MCP service tools were removed from the active surface
- socket/API runtime operations are resource-native; top-level `/services` is no longer exposed
- config now requires `version: 2`; the v1 migration path and `config migrate` command were removed

## Important Files

- [internal/cerbapi/resource_runtime_service.go](/Users/chrispian/Projects-apps/cerberus/internal/cerbapi/resource_runtime_service.go)
- [internal/cerbapi/resource_monitor.go](/Users/chrispian/Projects-apps/cerberus/internal/cerbapi/resource_monitor.go)
- [internal/connector/local/dev_session.go](/Users/chrispian/Projects-apps/cerberus/internal/connector/local/dev_session.go)
- [internal/pipeline/resolve.go](/Users/chrispian/Projects-apps/cerberus/internal/pipeline/resolve.go)
- [internal/app/app.go](/Users/chrispian/Projects-apps/cerberus/internal/app/app.go)
- [cmd/cerberus/main.go](/Users/chrispian/Projects-apps/cerberus/cmd/cerberus/main.go)
- [docs/adr/0001-local-runtime-backends.md](/Users/chrispian/Projects-apps/cerberus/docs/adr/0001-local-runtime-backends.md)
- [docs/adr/0002-resource-only-local-workload-model.md](/Users/chrispian/Projects-apps/cerberus/docs/adr/0002-resource-only-local-workload-model.md)
- [docs/plans/daemon-management-v2.md](/Users/chrispian/Projects-apps/cerberus/docs/plans/daemon-management-v2.md)

## Verified Before Pause

These passed at the end of the session:

```bash
go test ./internal/cerbapi ./internal/mcp ./cmd/cerberus
go build ./cmd/cerberus
```

Additional focused suites also passed earlier in the session for:

- `./internal/connector/local`
- `./internal/pipeline/...`
- `./internal/app`

## Remaining Architectural Debt

The operational cut-over is complete. Remaining debt is repo cleanup rather than runtime behavior:

- unused legacy packages still exist in-tree (`internal/service`, `internal/tui`, related docs/tests/comments)
- some architecture/docs references still describe the retired service/TUI model
- the hand-rolled MCP transport still exists and remains a separate modernization target

## Recommended Next Slice

Let the v2-only surface settle under real use, then delete dead legacy packages and stale docs in one cleanup pass.

## Useful Context For Resume

- The user explicitly wants a clean v2 break and is comfortable with disruption.
- They do not want energy spent improving `services:` or the TUI.
- Long-term GUI is preferred over TUI, but the immediate requirement is API completeness for future GUI clients.
- The architecture target is one shared execution/service layer with all clients as thin wrappers.

## Current Session Endpoint

The active v2 cut-over is complete: deploy/apply/resource-native guidance is in place, legacy operator surfaces were removed, config is strict v2, and Vanta memory/knowledge was updated with the new operating rule.

Current local-runtime policy is now:

- `dev` flows should use `dev_session` on repo-local ports
- `uat` flows should use `os_service` plus `run_from: artifact`
- `release` flows should stay on `os_service`, using promoted user-owned or system-owned artifacts rather than ad hoc dev servers

Artifact-backed resources with a declared `build:` command now also carry a repo-state freshness signal, so status can recommend `resource deploy` when Git commit or worktree drift makes the installed artifact older than the current source tree.

The next distribution-focused slice is defined but not yet implemented:

- pilot one app, likely `nanite`, through a full `dev` vs `release` split
- keep `dev` repo-backed with alternate ports and data roots
- add a canonical user-facing installed artifact path outside the repo for `release`
- script a macOS-first install/update flow so end users do not need the repo or local builds

Beta-release planning now lives in:

- [docs/plans/beta-release-plan.md](/Users/chrispian/Projects-apps/cerberus/docs/plans/beta-release-plan.md)

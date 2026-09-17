---
id: "CERB-CAP-103"
class: "capability"
name: "Artifact build to install join"
summary: "Builds a resource from its declared build strategy, installs the built binary into the Cerberus user area with a content-hash manifest, and reports artifact_stale with the verb that fixes it."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.8
confidence_label: "unit-tested end to end in artifact_test.go and build_strategy_test.go; no resource on this machine declares a build_strategy or run_from: artifact, and the daemon-side go build path is broken (GAP-140)"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/local/artifact.go"
tags:
  - "cerberus"
  - "class:capability"
  - "artifact"
  - "build"
  - "deploy"
  - "staleness"
  - "unproven"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-100"
    note: "the run_from: artifact half of the lane"
  - type: "depends_on"
    target: "CERB-CAP-102"
    note: "artifact mode is only meaningful under a supervisor that runs the installed copy"
  - type: "implements"
    target: "CERB-DEC-164"
    note: "staleness is source-binary content hash, not git state"
  - type: "implements"
    target: "CERB-DEC-166"
    note: "deploy refuses an ambiguous build-to-artifact join"
  - type: "implements"
    target: "CERB-DEC-165"
    note: "install copies to a fresh inode and renames"
  - type: "relates_to"
    target: "CERB-GAP-145"
    note: "apply and sync do not enforce the output contract deploy does"
  - type: "relates_to"
    target: "CERB-GAP-140"
    note: "go_standard builds initiated through the daemon cannot find go"
---

# Artifact build to install join

`run_from: artifact` separates what a resource runs from where its source lives. The installed copy sits at `~/.cerberus/apps/<project>/<resource>/bin/<resource>`, and `install-manifest.json` beside it records the source path, the source binary's SHA-256, the artifact path, the sync time, an optional git repo state and an `activated_hash`. `internal/connector/local/artifact.go` owns that join.

Staleness is content, not timestamps and not git. `Status` recomputes the source binary's hash and compares: a differing hash is `source_changed`, a missing file is `source_missing`, an unresolvable source is `source_unresolvable`, a changed resolution is `source_path_changed`, and an installed file whose bytes no longer match the manifest is `installed_artifact_changed`. `activation_pending` is a separate axis — the bytes are installed but no successful launchd activation has been recorded against that hash. `artifact_repo_state` is recorded for the operator's benefit ("what HEAD was this built from?") and explicitly excluded from the staleness computation, because any commit in a shared repo would otherwise mark every artifact in it stale while the binary was byte-identical.

That feeds `RecommendedStatusAction`, which is the only producer of the `artifact_stale` advice operators see. It returns nothing at all unless `mode: os_service` and `run_from: artifact`, then: missing artifact → `apply`; activation pending with fresh bytes and a non-stopped service → `inspect`, with reasoning that a running PID does not confirm the installed image; stale while active → `apply`; stale while stopped → `sync`. `RecommendedNextStep` turns each into a sentence naming the command.

The build half is `build_strategy:` with three kinds — `go_standard` (requires a `rules.output`, supports a GOOS/GOARCH matrix with archiving and checksums), `make_standard`, and `legacy_command` (the translation target for the deprecated `build:` field, so an unmigrated resource still builds instead of silently dropping out of the runtime). `deploy` takes a build lock, writes `logs/build.log` on success and failure alike so a build is diagnosable after the fact, optionally runs `make install` when a Makefile with an `install` target exists, and then — for artifact resources — stats the resolved source path and fails loudly if the build produced nothing there. That guard exists because a "successful" build that wrote somewhere else would silently sync a stale artifact.

Maturity is `partial`, and honestly so. The unit tests are good. But no resource in `~/.cerberus/config.yaml` declares a `build_strategy` or `run_from: artifact`, `cerberus resource list` shows an empty ARTIFACT column for all ten rows, and the `go_standard` path is actually broken when invoked through the running daemon (CERB-GAP-140). Nothing in this capability is exercised in production on this machine.

## What it owns

- install layout derivation (`DefaultInstallLayout`)
- content-hash artifact sync with manifest (`artifactInstaller.Sync`)
- staleness classification and activation-pending tracking (`Status`, `recordArtifactActivation`)
- the three build strategies and the deprecated `build:` translation
- the build lock, always-on build log, and post-build freshness guard
- `make install` probing and skip semantics (`RunInstall`)
- artifact_stale advice and next-step text (`RecommendedStatusAction`, `RecommendedNextStep`)
- the background drift cache the list path reads instead of probing per poll

## What it does not own

- source freshness: `apply` explicitly says no build ran and source freshness was not checked
- staleness advice for dev_session resources — that is the separate, mtime-vs-launch-time `RecommendedDevSessionAction`
- advice for `run_from: workspace` resources: `RecommendedStatusAction` returns nothing for them
- the output contract on the apply and sync paths — only deploy calls `ValidateDeployOutput`
- providing a toolchain: the build shells out to `go`, `make` or an arbitrary argv and assumes it resolves
- any notion of a remote or shared artifact store; everything is local to this user's home

## Vendor dependencies

- go toolchain
- make
- git (repo-state record only)

## Where it lives

- `internal/connector/local/artifact.go`
- `internal/connector/local/build_strategy.go`
- `internal/connector/local/build_lock.go`
- `internal/connector/local/build_log.go`
- `internal/connector/local/install.go`
- `internal/connector/local/activation.go`
- `internal/connector/local/status_advice.go`
- `internal/cerbapi/drift_cache.go`

Surfaces: cli, socket, mcp, console. Entry points: cerberus resource deploy|sync|apply|status|inspect <id>; cerberus resource ensure-fresh <id>.

## Recorded reasoning

- `docs/adr/0002-resource-only-local-workload-model.md`

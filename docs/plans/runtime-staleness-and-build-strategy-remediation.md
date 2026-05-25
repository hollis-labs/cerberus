# Plan: Stop runtime staleness & finish the build_strategy migration

**Status:** proposed (investigation complete 2026-05-25)
**Origin:** Tether daemon-reload incident (`/Users/chrispian/dev/agent-os/inbox/cerberus/2026-05-25-tether-daemon-service-reload-registry-urn-compat.md`) + recurring "stale binary / frontend-backend out of sync" reports.
**Related:** Vanta followups `project_config_forward_compat_unknown_fields`, `surface_skipped_configs_in_list_commands`, `webui_drift_status.selfexec_install_after_build_self_deploy`.

## Problem

Services repeatedly end up **stale** — the running process is not the latest
source — and humans/agents can't tell or can't fix it:

- Stopping/restarting in the GUI leaves the binary stale.
- Agents "use cerberus" and still end up stale.
- Frontends/backends drift out of sync.
- Agents verify with `go test ./...` / `make build` and assume the service updated.

This sits on top of two migration gaps surfaced the same day:
1. `registry_urn` was written into configs before the reader binary supported it (fixed 2026-05-25 by redeploying CLI + daemon to HEAD).
2. The deliberate `build:` → `build_strategy:` hard break (shipped in `3b0f36b`, build_strategy added 2026-05-24) was **not applied to all configs** — 13 of 18 still used `build:`. A decode-time back-compat shim (`legacy_command` strategy) was added 2026-05-25 so they keep working, but the configs still need real migration.

## Root causes (from 2026-05-25 investigation, ranked)

1. **`reload` / GUI "Restart" ≠ rebuild.** They `launchctl kickstart -k` the *existing installed artifact*. Only `deploy` rebuilds + re-syncs. Most common everyday cause. (`internal/connector/local/launchd.go:175-192`, `internal/cerbapi/resource_runtime_service.go:553-576`)
2. **Editing source without deploying leaves the artifact legitimately stale**; no restart fixes it — only `deploy`. (`status_advice.go`, `artifact.go:199-206`)
3. **Build↔install are decoupled — joined only by convention.** The install/sync step copies `command[0]` (`resolveArtifactSource(spec) = spec.Command[0]`), NOT the build's actual output. `go_standard` works only because `output` is hand-kept equal to `command[0]`; `make_standard`/`legacy_command` declare **no** output binary, so if a build writes anywhere other than exactly `command[0]`, **deploy reports success while the artifact stays stale, with no error.** (`internal/connector/local/artifact.go:244-260`, `build_strategy.go:216-253`)
4. **`install_after_build` (`make install`) is confused with the artifact copy.** It runs a Makefile `install` target (silently skipped if none) and never copies into `~/.cerberus/.../bin/`. The real copy is always `Sync`. (`internal/connector/local/install.go:35-57`)
5. **No staleness detection for `dev_session` resources** — `art.Stale` is hard-false for non-artifact run_from and `RecommendedStatusAction` early-returns empty unless `os_service`+`artifact`. This is exactly the "frontend/backend out of sync" case, and status is blind to it. (`status_advice.go:12`, `artifact.go:80,152`)
6. **GUI gaps.** TUI (legacy lane) has **no deploy** action and **never** shows staleness; `r`=restart relaunches the same binary → strongest false confidence. Web UI's row-level **"Restart" calls `reload`** (no rebuild); the only rebuild (**Deploy**) is buried in the detail dialog, though a drift chip is shown. (`internal/tui/model.go:204-249`, `internal/tui/view.go:214-333`, `web/src/pages/resources.tsx:226-241,535-546`)
7. **MCP descriptions are the weakest layer** (the one agents consume): deploy/reload/apply don't cross-reference, don't mention `run_from: artifact`, and the status tool doesn't tell the agent to act on `recommended_next_step`. No single "make current" tool. (`internal/mcp/tools_resources.go`)
8. **No crisp prohibition rule** where an agent reads first (now added to `CLAUDE.md` / `AGENTS.md` / `.agent-ops/project.yaml`, 2026-05-25).
9. **`status` socket basic/list probe skips drift computation** (`artifact_stale: null`); the TUI table relies on the 60s `DriftCache`. Cold cache / no daemon → under-reported staleness. (`artifact.go:233-237`, `drift_cache.go`)

Not a cause: `selfexec` is now dead code (removed CW-20260519-0053) — latent footgun only.

## The real solution (layered)

### Layer 0 — Docs & agent guidance (DONE 2026-05-25)
- `CLAUDE.md`: CRITICAL "deploy, don't build/restart" callout (mirrors the `port: 0` block).
- `AGENTS.md`: top "Golden rule: to update a running service, DEPLOY".
- `.agent-ops/project.yaml`: explicit STALENESS-RULE usage example.

### Layer 1 — Make a successful deploy provably fresh (close the silent-stale hole)
- Build strategies must declare the produced binary; the install/sync step must use the build's actual output (wire `BuildResult.Output`/`Artifacts` → `resolveArtifactSource`) instead of blindly copying `command[0]`.
- After deploy, **verify** the installed artifact hash changed when the source changed; if a "successful" deploy left the artifact stale, fail loudly instead of reporting success.
- Add an `output`/`artifact` rule to `make_standard`/`legacy_command` so install knows what to copy.

### Layer 2 — Semantic "make current" surface (CLI + MCP)
- Add `cerberus resource ensure-fresh <id>` (alias e.g. `make-current`) and an MCP `cerberus_resource_ensure_fresh`: read status → if stale/not-installed, deploy; else apply/reload as appropriate; idempotent. Reuse `internal/connector/local/status_advice.go` (`RecommendedStatusAction`). One obvious thing agents call.
- Harden existing MCP descriptions: cross-reference deploy↔reload↔apply, mention `run_from: artifact`, and have `cerberus_resource_status` tell the agent to act on `recommended_next_step`. Expose `--install-after-build` parity.

### Layer 3 — dev_session staleness
- Detect "process started before the last build / source changed since start" for `dev_session` resources and surface a recommended action (restart), so frontend/backend drift is visible.

### Layer 4 — GUI co-location
- Web UI: co-locate **Deploy** with the row-level action set; make the drift chip's fix one click. TUI is frozen (legacy) — at minimum surface a staleness hint, or steer to the web UI / CLI.

### Layer 5 — Finish the build_strategy migration (the 13 configs)
Migrate from the `legacy_command` shim to first-class `build_strategy` for every
config still using `build:`. **Cerberus leads its own; other apps in a later
window when no one is actively working on them** (see Vanta memory
`cerberus_lead_build_strategy_migration_13_configs`).

`make build` → `make_standard`; `go build …` → `go_standard`; bash/npm scripts →
`legacy_command` with an explicit `output` (or a future first-class node/script
strategy). Each migration must verify the artifact actually refreshes (Layer 1).

Configs still on `build:` (2026-05-25): torque, hadron, sigil, stack-explorer,
tesseract (conduit), nanite, tangent, fragments-engine (×2), tether, tachyon,
agridd (×2), sysop-ui, glyph. Already migrated: cerberus.

## Sequencing
Layer 0 done. Layer 1 is the highest-value code fix (kills silent stale-after-deploy). Layer 2 makes the right action obvious. Layers 3–4 broaden coverage. Layer 5 is operational cleanup, owner-paced.

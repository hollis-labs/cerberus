# Conversion notes: Cerberus to Substrate

Old-to-new mapping for operators, the console and callers. Started in slice 1 (CW-20261114-0108); each slice
adds its rows. The plan is `docs/substrate-port-plan.md`.

## Gaps (need a decision)

| Gap | Status |
|---|---|
| BOM does not pin `libs/mesh/eventbus` (event export); `substrate pins check` fails on that line. | Coming in @substrate-manager's BOM raise (CW-20261114-0093). |
| BOM does not pin `libs/security/webauthn` (passkey approvals). | Not pinned until the owner decides whether passkey approvals stay. Every package that depends on it is out of this branch (`docs/architecture.md`). |
| `substrate scan-public --ref HEAD` reports findings carried from `main` (test fixtures with fake keys, example paths and addresses in docs; tree and history). None come from the port's own files. | Needs triage: fix or allowlist with a reason, owner sign-off for history. |
| Code carried from `main` has lint findings that `main` never gated in full. | CI lints new issues only; each slice cleans what it ports. |

## Paths

| Old | New |
|---|---|
| `~/.cerberus/` (state tree: config, registry, audit, approvals, plugins, tokens) | Data directory `CERBERUS_DATA_DIR`, default `~/.local/share/cerberus-substrate/` (0700), a sibling of the old `~/.local/share/cerberus/`. The old tree is imported by `cerberus migrate-from` (slice 10), never opened in place: the new build refuses to start with its data directory or database anywhere under `~/.cerberus` or `~/.local/share/cerberus`, and a refused start changes nothing there. |
| `~/.local/share/cerberus/workspaces/default/main.db` | `<data dir>/cerberus.db` (`database.sqlite.path`) |
| `~/.cerberus/config.yaml` (`--config`) | Manifest defaults plus the `SUBSTRATE_CONFIG` override file |
| `~/.cerberus/apps/` | Unchanged, adopted in place: `cerberus.install_root` |
| `~/.cerberus/cerberus.sock`, `daemon.lock`, `cerberus.pid` | None. One process per data directory, held by the database open lock (`<db>.open.lock`) and the unit. |
| `~/.cerberus/pids/<id>.{pid,meta}`, `~/.cerberus/locks/` | `<data dir>/pids/`, `<data dir>/locks/`. Never read or written by the new build; old's running sessions are listed in `standby.d/sessions.json` and started under new at D7. |
| `~/.cerberus/paused`, `paused.<id>` | `pause_state` in the class A store (migrated by slice 10) |
| `~/.cerberus/runtime/applied.json` | `applied_definitions` in the class A store, same digests (key `<data dir>/audit/.digest_key`) |
| `~/.cerberus/brakes/` | `<data dir>/brakes/` (copied if present) |
| `~/.cerberus/hosts.yaml` | Unchanged, adopted in place (`<adopt_root>/hosts.yaml`); Outpost `token_file` paths keep pointing into the adopted root and are read in place on every call |
| `~/.cerberus/state/hosts.json` | `<data dir>/state/hosts.json` in supervise mode (copied by slice 10); observe mode reads the old file in place and never writes it |
| `~/.cerberus/events/` (export outbox and hub cursor) | `<data dir>/events/`; export is off (`cerberus.event_export.enabled: false`) |
| `~/.cerberus/events-tokens/` | Unchanged, adopted in place; written by the renewal job, read only by the hub relay through the eventbus environment when export and its hub are on |

## Environment

| Old | New |
|---|---|
| `CERBERUS_DB_PATH`, `--db` | `database.sqlite.path` in the override file |
| `CERBERUS_WORKSPACE` | Dropped: one database per data directory |
| `HOME`-rooted `~/.cerberus` | `CERBERUS_DATA_DIR` (the only environment setting the app reads) |
| `CERBERUS_DAEMON_CHILD` | Dropped: no self-fork |

The remaining old variables (`CERBERUS_PRINCIPAL`, `CERBERUS_DOCKER_PATH`, `CERBERUS_SSH_KEY_FILE`,
`HOLLIS_EVENTS_URL`, `HOLLIS_AUTH_TOKEN_FILE_CERBERUS`) are mapped in the slice that ports their code.

## Network

| Old | New |
|---|---|
| Daemon on a unix socket | HTTP on `httphost.addr`, default `127.0.0.1:4786` (loopback only) |
| Console `cerberus web` on `127.0.0.1:4783` | Same process as the API (slice 11) |
| `cerberus mcp-http` on `127.0.0.1:4785` | `/mcp` on the same host (slice 9) |
| — | `GET /healthz`: `{"status":"ok","checks":{"database":"ok"}}` |
| Socket routes `/health`, `/resources…`, `/hosts` (tachyon, tether sysop) | The legacy-route set under `/api` with the same shapes, behind an API key (`docs/substrate-port/legacy-routes.md`, an approved cross-app contract) |
| Same-uid trust on the socket | Substrate identity: the owner signs in (created offline with `cerberus identity setup`); machine callers present an API key (`cerberus identity key create`) with scopes `cerberus.read`, `.operate`, `.admin`. `cerberus.approve` is the owner's console session only and is refused on any key |

## Settings (slice 8)

| Old | New |
|---|---|
| `config.yaml` `host_monitor:` (same keys) | `cerberus.host_monitor` in the override file. Probing and the janitor run in supervise mode only; probing only when `enabled` |
| `events.yaml` `export.{enabled, grace_seconds, interval_seconds, hub.enabled}` | `cerberus.event_export.{enabled, grace_seconds, interval_seconds, hub}`, off by default; `migrate-from` transforms `events.yaml` |
| The scheduling core, built and never started; no key | `cerberus.scheduler.enabled` (default `false`). Enabled, it refuses to start while its `gosched_*` or `cerberus_schedule_*` tables hold rows, naming them, unless `cerberus.scheduler.allow_existing: true`; a refusal fails its `/healthz` check and never stops Cerberus |
| — | `/healthz` gains `telemetry`: an error while host-monitor writes are held in memory because the Telemetry store fails (up to 1000, oldest dropped and counted) |
| ssh hosts probed through the ssh connector | Not probed until the ssh connector arrives (slice 7): skipped with a warning, never marked offline |

## Commands

| Old | New |
|---|---|
| `cerberus daemon --foreground` | `cerberus serve` (also the default with no command) |
| — | `--help`, `version`, `config`, `health`, the read verbs and unknown words start only the modules that read config: no data directory, database or signing key is created (safe to run on the operator's account) |
| — | `cerberus identity setup` (offline owner), `cerberus identity key create` (refuses `*`, `cerberus.*` and `cerberus.approve`) |
| `cerberus resource list` / `resource status <id>` / `status` over the socket | The same verbs, flags and output, over HTTP to `/api` with the API key in `cerberus.cli_key_file` (default `~/.config/cerberus/cli.key`). `status` keeps the old text layout and section labels; sections later slices own print `unavailable (slice N)`. **Not parity:** `status -o json` is a flat `{section: line}` object, not the old nested `statusReport`. The key file must be mode 0600 and owned by the caller, and the address must be loopback |
| Every other verb | Not on this branch yet; each returns in its slice with the same name and flags (plan section 6). |

## Post-adoption improvements

Steps to take after the new build is accepted. None blocks the cutover.

- Adopt Substrate's `modules/scheduler` through `Engine(app)` and `UseRunner` (option B) when the scheduler is turned
  on. Today the scheduling core builds its own engine over the same tables, so the module stays out of the manifest
  (lead's decision, slice 8).
- Narrow tachyon's Cerberus key by dropping `cerberus.admin` once tachyon no longer uses `remove` (legacy route L8;
  `docs/substrate-port/legacy-routes.md` section 4).

## Resource verbs (slice 5)

| Old | New |
|---|---|
| `cerberus resource {apply,deploy,reload,stop,sync,remove} <id> --ack` over the socket | L7/L8 `POST /api/resources/{id}/{verb}` with `{"acknowledged":true}`; same `OpResult` and the same 200 `success:false` for a failed run |
| No `--ack` | 409 `acknowledgment_required`, before the resource is looked up (unchanged) |
| `--approval`, `--plan`, the confirm route, a lockdown or freeze | Until slice 6: refused, 428 or 423 `approvals_unavailable`. Run it with the old build, or without that option. |
| Supervise mode with a policy in force | Refused at start until slice 6 |
| A self-mutation guarded by the daemon's executable path and labels | Guarded by `cerberus.self_resource_id`: every verb on it is refused; upgrade with fleet-install |

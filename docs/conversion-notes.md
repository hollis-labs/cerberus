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
| `~/.cerberus/hosts.yaml` | Unchanged, adopted in place (`<adopt_root>/hosts.yaml`); Outpost `token_file` paths keep pointing into the adopted root and are read in place on every call. `migrate-from` leaves it there and the D3 manifest excludes it, like `registry.yaml`: the new build edits it while it runs, and old reads the same format, so a rollback keeps those edits |
| `~/.cerberus/hosts.history.jsonl` and its rotations | Unchanged, in place beside `hosts.yaml` in the adopted root, where the new build appends to it. Not copied by `migrate-from` and excluded from the D3 manifest, for the same reason as `hosts.yaml` |
| `~/.cerberus/state/hosts.json` | `<data dir>/state/hosts.json` in supervise mode (copied by slice 10); observe mode reads the old file in place and never writes it |
| `~/.cerberus/events/` (export outbox and hub cursor) | `<data dir>/events/`; export is off (`cerberus.event_export.enabled: false`) |
| `~/.cerberus/events-tokens/` | Unchanged, adopted in place; written by the renewal job, read only by the hub relay through the eventbus environment when export and its hub are on |
| `~/.cerberus/plugins/`, `plugin-data/`, `plugin-cache/` | Unchanged, adopted in place (`<adopt_root>/…`): the reviewed bundles at their recorded paths and digests, each plugin's data and cache directories |
| `~/.cerberus/plugin-connectors.json` | Unchanged, shared in place (`<adopt_root>/plugin-connectors.json`), same format (version 2). Supervise mode reads it at start and writes it, under old's lock (`.lock`, flock) with an atomic 0600 rename, only when the operator loads, unloads, reloads or uninstalls a plugin; stopping plugins at exit does not rewrite it. Only one build runs plugins at a time. `migrate-from` leaves it in place (reporting the bundle digests it records) and the D3 manifest excludes it, like `registry.yaml`, so a rollback keeps new's edits |
| `~/.cerberus/credential-issuance/<id>/<state>.json` (receipts) | `credential_issuance_receipts` in the class A store; inserting the `preparing` receipt is the exclusive reservation of an issuance id (migrated by slice 10). The issued secrets stay files in the adopted root, `<adopt_root>/credential-issuance/secrets/issued/<id>/<name>` |
| `~/.cerberus/connector-secrets.yaml` | `<config dir>/cerberus/connector-secrets.yaml` (`$XDG_CONFIG_HOME/cerberus`, beside the policy), copied at 0600 by slice 10 |
| `~/.cerberus/connector-config.yaml` | `cerberus.connectors` in the override file (Settings) |
| `transfers.root` (default `~/.cerberus/transfers`) | `<adopt_root>/transfers`, fixed; the old config key is not read. `migrate-from` refuses an old config that set another root, and reports the default or an unset key |
| `~/.cerberus/oauth/`, `alerts/` | `<data dir>/oauth/`, `<data dir>/alerts/`: copied inert by `migrate-from` and reported; nothing in the new build reads them yet |

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
| `cerberus mcp-http` on `127.0.0.1:4785` (OAuth resource server, `cerberus:read`-style scopes) | `/mcp` on the same listener as `/api` (`127.0.0.1:4786`), behind an API key holding `cerberus.read`; each tool then needs its own `cerberus.*` scope. The old colon scopes and `--no-auth` are gone |
| — | `GET /healthz`: `{"status":"ok","checks":{"database":"ok"}}` |
| Socket routes `/health`, `/resources…`, `/hosts` (tachyon, tether sysop) | The legacy-route set under `/api` with the same shapes, behind an API key (`docs/substrate-port/legacy-routes.md`, an approved cross-app contract) |
| Socket `GET /projects`, `GET /hosts/{name}`, `POST /hosts/{name}/probe`, `GET /hosts/{name}/status`, `/hosts/_status`, `/hosts/{name}/samples`, `/hosts/_monitor`, `/hosts/_events` | The same under `/api` with the same shapes, all `cerberus.read`. In supervise mode each host read is recorded as the old host operation (an audited read); a probe measures over the outpost with the token file read in place. In observe mode a probe answers 409 `observe_mode` (it would write the old build's state file) and the reads are not recorded. **Dropped:** the events stream (`Accept: text/event-stream` on `/hosts/_events`): no client calls it (`docs/substrate-port/inventory-clients.md` lists no caller); the JSON route and `cerberus_host_events` are carried. If a caller turns up, it comes back |
| Same-uid trust on the socket | Substrate identity: the owner signs in (created offline with `cerberus identity setup`); machine callers present an API key (`cerberus identity key create`) with scopes `cerberus.read`, `.operate`, `.admin`. `cerberus.approve` is the owner's console session only and is refused on any key |

## Settings (slice 8)

| Old | New |
|---|---|
| `config.yaml` `host_monitor:` (same keys) | `cerberus.host_monitor` in the override file. Probing and the janitor run in supervise mode only; probing only when `enabled` |
| `config.yaml` `build.install_after_build` (default `true`) | `cerberus.build.install_after_build` (default `true`, old's): the global default of `resource deploy`'s install step, below the resource's own `install_after_build` and the CLI's `--install-after-build`. The registry's project configs no longer set it. `migrate-from` carries an old value into the fragment |
| `config.yaml` `web.public_url` | No key of its own. `migrate-from` reports the value and writes it into the fragment as a comment: the console's public origin goes in `identity.allowed_origins` with `identity.trusted_proxy: true` behind the TLS proxy (the console's host section) |
| `events.yaml` `export.{enabled, grace_seconds, interval_seconds, hub.enabled}` | `cerberus.event_export.{enabled, grace_seconds, interval_seconds, hub}`, off by default; `migrate-from` transforms `events.yaml` |
| The scheduling core, built and never started; no key | `cerberus.scheduler.enabled` (default `false`). Enabled, it refuses to start while its `gosched_*` or `cerberus_schedule_*` tables hold rows, naming them, unless `cerberus.scheduler.allow_existing: true`; a refusal fails its `/healthz` check and never stops Cerberus |
| — | `/healthz` gains `telemetry`: an error while host-monitor writes are held in memory because the Telemetry store fails (up to 1000, oldest dropped and counted) |
| ssh hosts probed through the ssh connector | Unchanged (slice 7): probed through the admin lane's ssh connector, resolved per probe, each probe's credentials resolved as a read of its `reach.ssh` resource |
| `connector-config.yaml` (plugin id: `fields`, `mcp.expose`, `limits`) | `cerberus.connectors`, the same shape under one key; an unknown key refuses the start, naming the block. Read at start: an edit takes a restart, where old re-read the file on every load. `config_sha256` on a plugin is the block's fingerprint, not the old file's |

## Commands

| Old | New |
|---|---|
| `cerberus daemon --foreground` | `cerberus serve` (also the default with no command) |
| `cerberus mcp` (stdio, dialling the daemon socket) | `cerberus mcp`: the same tools (main's catalog, same names and input schemas), forwarding to the serving Cerberus with its own key, named `cerberus-mcp` (read, operate, admin; created at D6), from `cerberus.mcp_key_file` (default `~/.config/cerberus/mcp.key`); it creates nothing. The gate records its calls via `mcp_stdio` with the MCP client's name, HTTP `/mcp` via `mcp_http`, and the CLI's key (named `cerberus-cli`, D6) via `cli`. No serving Cerberus: each call fails naming the address. **Not yet:** the posture instructions main sent on initialize (Substrate gap CW-20261115-0043) |
| — | `--help`, `version`, `config`, `health`, the read verbs, `run-secrets` and unknown words start only the modules that read config: no data directory, database or signing key is created (safe to run on the operator's account) |
| — | `cerberus identity setup` (offline owner), `cerberus identity key create` (refuses `*`, `cerberus.*` and `cerberus.approve`) |
| `cerberus resource list` / `resource status <id>` / `status` over the socket | The same verbs, flags and output, over HTTP to `/api` with the API key in `cerberus.cli_key_file` (default `~/.config/cerberus/cli.key`). `status` keeps the old text layout and section labels; sections later slices own print `unavailable (slice N)`. **Not parity:** `status -o json` is a flat `{section: line}` object, not the old nested `statusReport`. The key file must be mode 0600 and owned by the caller, and the address must be loopback |
| `cerberus lockdown [--reason]`, `freeze --scope k=v… [--reason]`, `freeze list`, `breaker list` over the socket | The same verbs, flags and output, over HTTP to `/api/brakes` with the CLI's key (engaging needs `cerberus.admin`). `status` leads with the old `!!! LOCKDOWN`, `!!! FREEZE` and `!!! BREAKER` lines when the server holds brakes |
| `cerberus lockdown --off [--approval]`, `freeze --off <id> [--approval]`, `breaker reset <id>` (a person on a terminal, typed phrase) | **Changed:** old lifted a brake and reset a suspension at a terminal with a typed phrase; now the owner does it on the console, or the CLI does it with `--approval <id>` naming an approval the owner decided on the console (`breaker reset` gains `--approval`). Asking without `--approval` still needs a terminal and the typed phrase, and the server answers 409 `approval_pending`; the CLI prints the approval to approve on the console and the `--approval` command to run after; with `--approval` no terminal or phrase is asked, so a script can finish it. Engaging is unchanged (anyone with admin) |
| Engaging a brake, or resetting a suspension, with the daemon down (written to the brake store in-process) | Not ported: the verbs need the serving Cerberus. An in-process path would be a second writer of the audit chain beside a running server, and an in-process reset would have no person to check |
| `cerberus resource apply\|deploy\|reload\|stop\|sync\|remove <id> --ack [--approval <id>]` (deploy: `--install-after-build`, `--no-install-after-build`), `resource plan <id> <verb> [--ack]`, `resource logs <id> [-n] [--stream] [--approval]` over the socket, or in-process with the daemon down | The same verbs, flags and output over L6, L7 and L8 with the CLI's key (slice 7b). A failed run prints old's build and install output on stderr and exits 1 with its error. `-o json` prints the `OpResult` (new; old had no `-o` on these). **Changed:** no in-process run with the server down (every attempt is recorded by the server); `approval_pending` exits 1 with the approval to approve on the console and the `--approval` command; old's terminal confirm and `--break-glass`/`--reason` are not carried (obsolete, below). Runbook D7's `resource apply <id> --ack` is a process test (`TestRunbookD7ResourceApplyStartsTheDevSession`) |
| `cerberus resource show\|inspect\|doctor <id>`, `resource ensure-fresh <id> [--force] [--ack] [--approval <id>] [--install-after-build\|--no-install-after-build]` | The same verbs, flags and output. `show` is old's local config view, from the registry in the adopted root (the resolve the server does), Config redacted. `inspect` and `doctor` read `GET /api/resources/{id}/inspect` and `/doctor` with the CLI key; until supervision answers the full read they print what the server has (inspect's status fields, doctor's summary with no checks); an unsupervised resource is old's "does not apply" error. `ensure-fresh` is old's client-side composition: `--force` deploys; otherwise it runs the status's recommended deploy, apply or sync, passing `--ack`, `--approval` and the install override to that call. **Changed:** this build's status does not compute drift advice yet (slice 8b), so with no advice it answers `pending (slice 8b)`, exit 1, nothing changed, instead of old's "no built-binary drift detected". `--break-glass`/`--reason` are obsolete as on the other verbs. inspect, doctor and ensure-fresh need the server; show does not |
| `cerberus secrets set <service>/<key>` (in-process keyring write, `beginAudit` only) | The same verb, terminal only, the value pasted with no echo (raw mode, Ctrl-C cancels), old's output and unload/load hint, now over `POST /api/secrets/{service}/{key}[/plan,/confirm]` (admin) with `GET /api/secrets/{service}/{key}` (read, presence only) for the "already has a value" warning. **Changed:** a gated admin write, recorded as `secrets.set` with `config {name}` and old's operation contract (effect admin, target kind `secret` from `name`); the plan binds the value by its keyed digest. Any well-formed name is accepted (old's regex), declared by an installed connector or not, so a backend's credential is set before its plugin is installed. Keyring effect unchanged (service `cerberus`, account `<service>/<key>`). **Owner only** (the lead's ruling (a), as for the brakes and plugin install; old allowed it from any interactive terminal): the owner signed in on the console, or the CLI's key with `--approval <id>` naming the out-of-band approval its first run opened (409 `approval_pending`, printed with the approve-on-console instruction) once the owner approved it there; paste the same value, since the approval binds its digest. An MCP client is refused 428 and opens no approval. The body carries `acknowledged` (the CLI sends it; a write without it is 409 `acknowledgment_required`). Needs the server |
| `cerberus register <path>`, `deregister <owner>`, `registry list [-o json]`, `registry health` | The same verbs and output. They read and write `<adopt_root>/registry.yaml` in place, directly, as old's CLI did (no gate, no audit record, old's atomic rename; old had no lock), so registering works with the server down; the server reads the index fresh. The console's gated register route is slice 11's |
| `cerberus connectors exec\|plan <id> <op>` (`--arg`, `--arg-json`, `--input`, `--dry-run`, `--ack`, `--approval`), `connectors plugin managed list\|load\|unload\|uninstall\|health` | The same verbs, flags and JSON output over the admin routes. `--break-glass`/`--reason` on exec are obsolete (below) |
| — | `cerberus import-standby --from <archive> [-o json]`: the standby sidecar import (F2, section "Standby sidecar (F2)" below). Needs no server |
| — | `cerberus export-legacy --since <handoff> [--old-home] [--old-db] [--old-anchor-account] [--only telemetry] [-o json]`: the rollback of `migrate-from` (section "Rollback (slice 10b)" below). `--check-legacy-files --d3-copy <dir>` is runbook step 0. Needs no server; exits 0 when every step is done, 3 when only the class B step is pending, 1 for a refusal or an error |
| Every other verb | Listed with its owner in the CLI verb parity table (below); each returns with its slice with the same name and flags, or is recorded obsolete. |

## CLI verb parity

`cmd/cerberus/cli_parity_test.go` walks this build's command tree against main's full cobra tree
(`cmd/cerberus/testdata/clitree/main.tree`, generated on a checkout of main by
`tree_gen_on_main.go.txt`). Every old path and flag is here or has a decision with an owner and a
reason; an unknown old verb, or a decision for something now present, fails it. The decisions (62):

| Owner | Rows | What |
|---|---|---|
| obsolete | 26 | Passkey verbs (`approvals enroll`, `keys`, `enrollment-policy`) and `approvals approve`, `deny`, `revoke`, `ack-break-glass` (decision 0.1: the owner decides on the console); one-shot `connectors plugin exec/health <dir>` (410 on main); `--break-glass`/`--reason` on `connectors exec`, the six resource verbs and `resource ensure-fresh`; `managed install --yes` |
| replaced | 3 | Root `--config` (the override file) and `--db` (`database.sqlite.path`); `managed load --accept-changes` (`managed review <id>`) |
| CW-20261115-0065 (slice 7c, the CLI follow-up) | 25 | Verbs over lanes this branch already serves: `approvals` list/show, `audit` query/tail/prune/reanchor, `component`, `config validate`, `connectors describe`, `write-plugin-prototype`, `data`, `docker`, `ssh` (meanwhile `connectors exec docker\|ssh <op>`), `host`, `path`, `pipeline`, `policy`, `posture`, `portfolio`, `project`, `schedule`, `validate`, `whoami` |
| slice 9 | 2 | `mcp`, `mcp-http` (and its `token` verbs) |
| slice 10 | 1 | `config migrate` (the import is `migrate-from`) |
| slice 11 | 1 | `web` (the console moves into the serving process) |
| slice 12; `$LEGACY` until F | 4 | `daemon` (replaced by `serve`), `install`, `uninstall`, `init` |

## Rollback (slice 10b)

`cerberus export-legacy --since <handoff>` writes what this build recorded after the handoff back into the old tree, in
the old build's formats, so the old binary takes over where this one stopped. The handoff is the D3 manifest
`migrate-from` kept in `<data>/migration/d3-manifest.json`, named by its body sha256 or a path to it; the migration's
own `report.json` must say it migrated against that manifest. `--old-home`, `--old-db` and `--old-anchor-account`
default to the tree the manifest was taken of; a rehearsal points them at a second copy.

**Stop the old daemon first** (plan rev 10). Old folds approvals and loads `applied.json` once at start, so a daemon
running across the export would chain from a stale seq and rewrite `applied.json` from a stale map. `export-legacy`
(and `--only telemetry`) refuses while old's single-instance lock `<old home>/daemon.lock` is held: it opens the file
read-only, tries a non-blocking flock and releases it at once (it never creates, writes or removes the file, and never
touches the socket), in preflight and again before each step writes. Start old again in standby after the export.

Progress is kept in `<data>/migration/export-state.json` (0600, tmp and rename), the one write into the new data
directory. The report states the exported approvals head (seq and hash) and the sha256 of `runtime/applied.json` as
the run leaves it, for runbook step 4.

Nothing is written unless the old tree matches the manifest (files by sha256, the anchor's head, `main.db` logically,
read live through its WAL), or the post-write state an earlier run recorded for the steps it finished
(`manifest.Expect`). The first mismatch is named. A step a crash or an error interrupted may have each of its files in
either state; anything else refuses. A finished run run again changes nothing.

| Store | What export-legacy does |
|---|---|
| `audit/YYYY-MM.jsonl` | New's file must start with old's bytes (sha256 of the first `size` bytes equals the manifest's); the rest is appended under old's `audit/.lock`. A month old never had (a rollover) is created whole, 0600. Month files new pruned stay. `.digest_key` is never touched and must be the same key. New's chain must verify against new's anchor first |
| audit anchor | Moved to new's last record (`{seq, hash, file, offset}`, under `cerberus-audit-anchor` and the old account) only after the old tree's chain verifies against that head, then checked against the keychain. New's own anchor is left |
| `approvals/events.jsonl` | Prefix-checked and appended, after new's store reads whole and `gate.VerifyApprovals` passes on it. Old's store must then read to new's next seq. Owner-session decisions go as they are: old never consumes one (its verifier refuses a `v: 1` seal) |
| `brakes/events.jsonl` | Prefix-checked and appended under the file's flock; old's store must fold to new's state |
| `paused`, `paused.<id>` | Set to new's pause set: a missing marker is created as old wrote it (`paused\n`, 0644), a marker new resumed is removed |
| `runtime/applied.json` | Old's map overlaid with new's digests (none is deleted), written as old wrote it (`MarshalIndent`, two spaces, no trailing newline, tmp 0600 and rename), and only when new added or changed one. New's digests must be keyed with old's key |
| `credential-issuance/<id>/<state>.json` | Each receipt new recorded becomes old's create-once file (0600, id, connector, operation, binding_digest, state, references, at in UTC, no newline); a new id's directory is created 0700. An existing file must already be those bytes |
| `state/hosts.json`, `state/overview_snapshots.json` | New's file copied back whole (tmp, rename, 0600), as old's writers write them; a file new does not have leaves old's |
| `main.db` host tables (class B) | Last, in one `BEGIN IMMEDIATE` transaction: samples and rollups old lacks are inserted (a sample past the manifest's newest; a rollup past old's newest bucket of its host and grain), events past the manifest's `sqlite_sequence`, status per host when new's `last_attempt` is newer, the larger rollup watermark. A row both sides hold with different values is a conflict. Verified before commit; nothing new's janitor deleted is removed from old. If the Telemetry store is unreachable or a row conflicts, the step is recorded pending and the command exits 3; `--only telemetry` finishes it |

Not reversed, reported: the config-dir `policy/` and the config fragment (a warning names the policy files that differ),
`oauth/`, `alerts/`, `events/`, `standby.d/`, and the files that stay in place (`registry.yaml`, `hosts.yaml`,
`hosts.history.jsonl*`, `plugin-connectors.json`).

`export-legacy --check-legacy-files --d3-copy <dir>` (runbook step 0) compares `plugin-connectors.json` with
`<dir>/cerberus-home/plugin-connectors.json` and, when it changed, parses it with the old build's own decoder, carried
verbatim from main (`readPluginConnectorState`, pinned to main's text by a test). A file old would refuse stops the
rollback.

`scripts/verify-exported-chain.sh` is the rehearsal: the old binary built from `git archive origin/main`, the fixture
migrated, a window of activity, `export-legacy` onto a second copy, then the old binary's `audit verify` there, and a
cut chain it must refuse.

## Standby sidecar (F2)

`cerberus import-standby --from <archive>` imports the old build's `standby.d/`, archived beside the old tree at F, as a
read-only sidecar log in `<data>/sidecar/standby/` (files 0400, directories 0500; an existing sidecar is refused). Every
source file is hashed before and after, and the source is never written. The standby chain is never anchored, so only
its hash chain is checked (`audit.Check` with no anchor); a chain that does not verify is refused. Old's standby fence
digests a verb's arguments with the standby chain's own `.digest_key`, while a read diverted through `audit.Switch` is
digested with the main key; plan rev 10 accepts both. For each standby verb record (connector `local`, apply, stop or
reload, via `standby`) the import recomputes the digest of `{"id": <resource>}` under the main key
(`<data>/audit/.digest_key`) and the standby key and reports `main`, `standby`, `both` or `neither`, never refusing on
it. `sessions.json` is parsed, and the whole report is written to `<data>/sidecar/standby/import-report.json`.

## Post-adoption improvements

Steps to take after the new build is accepted. None blocks the cutover.

- Adopt Substrate's `modules/scheduler` through `Engine(app)` and `UseRunner` (option B) when the scheduler is turned
  on. Today the scheduling core builds its own engine over the same tables, so the module stays out of the manifest
  (lead's decision, slice 8).
- Narrow tachyon's Cerberus key by dropping `cerberus.admin` once tachyon no longer uses `remove` (legacy route L8;
  `docs/substrate-port/legacy-routes.md` section 4).
- Owner-session approvals (decision 0.1, @archie's ruling (b)): a decision's seal is an HMAC under the audit digest key,
  so a process that can write the approvals store but not read the key cannot forge one. A process running as the
  owner can read the key and could still forge a decision; only the approvals hash chain and the anchored
  `approval_decided` audit record would show the edit. Accepted residual risk. Follow-up (c), with @substrate-manager:
  a per-app passkey, or a strict mode that refuses out-of-band approvals without one.

## Resource verbs (slice 5)

| Old | New |
|---|---|
| `cerberus resource {apply,deploy,reload,stop,sync,remove} <id> --ack` over the socket | L7/L8 `POST /api/resources/{id}/{verb}` with `{"acknowledged":true}`; same `OpResult` and the same 200 `success:false` for a failed run |
| No `--ack` | 409 `acknowledgment_required`, before the resource is looked up (unchanged) |
| `--approval`, `--plan`, a lockdown or freeze | Slice 6: as old. `approval_id` runs under that approval, `plan` answers with the plan, a lockdown or freeze is 423 `lockdown` or `frozen`. The confirm route is not in the legacy set: a `confirmed_plan_hash` on the plain route is ignored, as old's plain route did |
| Supervise mode with a policy in force | Slice 6: the policy is loaded from `<config dir>/cerberus/policy` and enforced as the snapshot says |
| A self-mutation guarded by the daemon's executable path and labels | Guarded by `cerberus.self_resource_id`: every verb on it is refused; upgrade with fleet-install |

## Admin lane (slice 7)

Supervise mode only: observe mode builds no admin lane, starts no plugin and writes nothing, and every route
below answers 503 `admin_unavailable`. Every write goes through the operation gate of slice 6, with the
contract's plan and confirm forms (`docs/substrate-port/console-api-contract.md` section 1).

| Old | New |
|---|---|
| Socket and console `GET /connectors`, `POST /connectors/{id}/operations/{op}[/plan,/confirm]` | `GET /api/connectors` (read), `POST /api/connectors/{id}/operations/{op}[/plan,/confirm]` (admin), same bodies (`config`, `dry_run`, `acknowledged`, `approval_id`, `plan`; `confirmed_plan_hash` on confirm). Refusals in the flat body with old's status per code. The break-glass form is not served |
| Console `GET /api/credentials`, `POST /api/credentials/{id}[/plan,/confirm]` (provider save) | The same under `/api` (read; admin). Values go to the keyring, service `cerberus`, account `<id>/<name>`, and are never recorded; a loaded plugin with that id is reloaded |
| Console and socket `/plugins/connectors`, `{id}/health`, `{id}/load`, `{id}/unload` (plain posts) | `GET /api/plugins/connectors`, `GET …/{id}/health` (read); `POST …/{id}/{load,unload,reload,uninstall}[/plan,/confirm]` (admin), gated like every admin write (the contract's change). `POST …/{id}/operations/{op}` (admin, `approval_id`); a `plugin_dir` is refused |
| Plugins restored at daemon start (automation, `daemon_start`), stopped by standby | Restored once the supervision lane runs, recorded as automation via `daemon_start`, never refused (a lockdown included); stopped at exit after the resource monitor (plan 4.4 step 5) without rewriting the state file |
| Keyring service `cerberus`, accounts `<id>/<name>`, `ssh/<resource-id>/key`, `<plugin-id>/<secret>`; `keychain://` and `keyring://` | Unchanged (a contract; `internal/admin` tests assert the names) |
| Plugins launched through `cerberus __plugin-exec` (rlimits) | Unchanged: the serving binary's own path |
| `cerberus connectors plugin managed install <dir> [--dev] [--yes]`, `review <id>`, `load <id> --accept-changes` (terminal-only install review, in-process, the typed plugin id) | **Changed:** old accepted a review in-process on the operator's terminal; now the owner does it on the console (a person typing the plugin id), or the CLI does it with `--approval <id>` naming an approval the owner decided on the console (`install` and `review` gain `--approval`). Asking without `--approval` still prints the review and needs a terminal and the typed plugin id, and the server answers 409 `approval_pending` with an out-of-band approval bound to the review's plan (the bundle digest); the CLI prints the approval to approve on the console and the `--approval` command to run after. `--yes` (permissive posture, unattended) is gone, and there is no in-process or local-trust path. Review semantics are old's: the bundle staged and digested, `audit.PluginReview` on the intent, `plugins/<id>/<digest-hex>/` in the adopted root, the `plugin-connectors.json` entry with `review`, `accepted_at` and `bundle_digest` under old's lock and atomic 0600 rename, suggested rules as a working file in `<config dir>/cerberus/policy/providers/<id>.yaml`. A changed bundle is accepted with `review <id>` (old's `load --accept-changes`); the new build has no `load` verb on the CLI |
| Install review over the socket or console (refused: in-process only) | `POST /api/plugins/connectors/install`, `POST /api/plugins/connectors/{id}/upgrade`, `POST /api/plugins/connectors/{id}/review`, each `[/plan,/confirm]` (admin). Body `{plugin_dir, dev, typed, approval_id}` (`review` takes no `plugin_dir`); the plan form answers the review as a plan (preview kind `review`) and accepts nothing; the answer is `ReviewResult` (`kind`, `review`, `changes`, `policy_change`, `proof`, `state`). The gate's routing comes first (scopes, brakes, policy), then the owner: a console person's `typed` equal to the plugin id (else 409 `acknowledgment_required`), or a key's `approval_id` (409 `approval_pending` until the owner approves; a pending, self-decided, spent or other caller's approval is refused; a changed bundle is 409 `plan_stale`); an MCP client 428 `approval_required`. Already reviewed: 409 `nothing_to_review`. Observe mode: 503 `admin_unavailable` |
| `cerberus run-secrets -- <program> [args...]` (the shim a unit with secret references runs) | Ported (slice 7b), same verb, arguments, 30s bound, `ResolveEnv` over the whole environment, `exec.LookPath`, backends closed before `execve` (the program keeps the shim's pid), an unresolved reference fatal (exit 1, the program not started, no value printed). It needs neither the server nor the database: it is not a data command, so only the config-reading modules start. Keyring references touch nothing else. A vault reference (`op://`, `keeper://`) builds, at the first one, `admin.NewProcessBackends`: `plugin-connectors.json` and `plugins/` in the adopted root registered and none loaded, the claimant loaded once and stopped before exec, a backend with a pending review or no digest refused, the state file never written, its own credential through the core chain (environment, `connector-secrets.yaml` in the config dir, keyring). Each load is recorded on `<data>/audit` through a gate with no broker as automation via `run_secrets` (surface `run_secrets`), never refused by policy; the posture a changed bundle is judged under is the applied policy, read from the policy dir. **Differences:** the settings (`cerberus.adopt_root`, `cerberus.connectors`) replace `connector-config.yaml`; the audit chain is `<data>/audit`, and a vault reference is refused when that chain does not exist yet (old created it): the server, not a service's start, creates it, so its digest key stays the migrated one. A config the CLI cannot read (the override file) now stops the shim before it resolves anything, where old read config only for a vault reference. An error prints once, redacted as old's was, as `cerberus: …` (the new CLI's prefix) rather than `Error: …` |
| `cerberus connectors plugin exec/health <dir>` (one-shot) | **Obsolete.** Main itself answers 410 `PluginDirRetired` on the socket and the console for a plugin directory (`internal/cerbapi/socket_server.go`, `internal/webui/server.go` on main); no client in `inventory-clients.md` calls it; it ran unreviewed code, against the owner-review model. `managed install --dev` with `connectors exec`, and `managed health <id>`, replace it |
| `--break-glass --reason` on `connectors exec` and the resource verbs | **Obsolete.** Break glass needs a person at the CLI (`gate.breakGlassOnCall`: principal human via `cli`). Under decision 0.1 no caller is one: a key is an agent via `api`, local trust an agent via `mcp_stdio`, the console a person via `web`. The admin routes and L7/L8 serve no break-glass form (`legacy-routes.md`). An approval comes from the owner on the console, then `--approval <id>`. The gate's hint for acknowledging an old break-glass use now names the console (`POST /api/approvals/{id}/ack-break-glass`), not `cerberus approvals ack-break-glass` |
| MCP plugin-tool sync (a loaded plugin's operations as MCP tools), MCP progress notifications during an operation | Out of this slice: with MCP (slice 9). `mcp_expose` is still reported per plugin |

## Audit, approvals and policy (slice 6)

| Old | New |
|---|---|
| `~/.cerberus/audit/` (hash-chained log, anchor in the keychain) | `<data dir>/audit/`, same format, continuing old's chain once copied (slice 10), same keychain item. Opened in supervise mode only |
| `~/.cerberus/approvals/events.jsonl` | `<data dir>/approvals/`, same chain and format; opened in supervise mode only. `approvals/passkeys/` is not used (decision 0.1) |
| `~/.cerberus/policy/` | `<config dir>/cerberus/policy/` (`applied.yaml`, `applied.sha256`), reloaded on change |
| Passkey approval (`/approvals/{id}/challenge`, WebAuthn assertion on decide) | The owner signed in to the console decides: `POST /api/approvals/{id}/decide` with `{"approve":true}`. The decision carries an owner-session marker and a seal (an HMAC under the audit digest key); `cerberus audit verify` fails on a seal that does not verify and lists pre-cutover decisions as legacy |
| `cerberus approvals approve` from a terminal (tty_confirm channel) | Not served: approvals are decided on the console session only. A confirm-on-the-call route is not in the legacy set |
| Socket `GET /approvals`, `GET /approvals/{id}`, `POST /approvals/{id}/{decide,revoke,ack-break-glass}` | The same under `/api`, same bodies; errors in the flat body with old's statuses. Decide, revoke and ack need `cerberus.approve` and the owner's console session (403 `user_required` for keys, minted tokens and local trust) |
| Socket `GET /brakes`, `POST /brakes/lockdown[/lift]`, `/brakes/freeze`, `/brakes/freeze/{id}/lift`, `/brakes/suspensions/{id}/reset` | The same under `/api`, same bodies and success shape (`BrakesView`); `GET` needs `cerberus.read`, the rest `cerberus.admin`. Gate refusals keep old's statuses (423, 428, 409 `approval_pending` with `details.approval`, 503 `audit_unavailable`); not engaged is old's 409, now coded `not_engaged`; errors in the flat body. A lift and a reset are the owner's: on the console, or a key's retry with `approval_id` naming an out-of-band approval the owner decided there (409 `approval_pending` until then; an MCP client 428); old's terminal-only lift is gone with passkeys (decision 0.1). The audit records are old's (`brake_changed`: `lockdown`, `freeze`, `lift_lockdown`, `lift_freeze`, `suspend`, `reset_suspension`). Observe mode: 503 `brakes_unavailable`. An API key is an agent to the circuit breaker, suspended by its key id |
| Socket `/resources/{id}/logs` gated as `read_sensitive` | L6 gated the same way in supervise mode (`?approval_id=` included); in observe mode a redacted read, logged, not recorded |
| Resource monitor restarts recorded as automation | Recorded the same way (`auto_restart: …`) |
| The interim approver of slice 5 (`approvals_unavailable`) | Gone. No shipped wiring builds a lane without the gate: `supervise.New` refuses supervise mode without one |
| Principal: CLI's own claim over the socket, `mcp_stdio` for `cerberus mcp`, `mcp_http` for mcp-http, the MCP clientInfo name as client | Console session: human via `web`. HTTP `/mcp`: agent via `mcp_http`; the `cerberus-mcp` key (`cerberus mcp`): agent via `mcp_stdio`; both with the MCP clientInfo name as client (unverified, as on main) and the principal id as session. The `cerberus-cli` key (D6): agent via `cli`, client `cerberus-cli`. Any other API key or minted token: agent via `api`, its principal id as client and session. The circuit breaker keys on kind, channel and session, so an MCP caller and the CLI never share one. **Changed:** every `cerberus mcp` client shares one breaker (the `cerberus-mcp` key's session), where main kept one per MCP client name; a trip suspends all stdio MCP clients together (it fails closed) until a person resets it |

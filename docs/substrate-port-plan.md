# Substrate port plan: Cerberus

Status: **plan, not started.** No port code merges before the program director approves this plan. Torque: CW-20261112-0099
(wave 3). Follows `hollis-labs/substrate` `docs/adopting-substrate.md` (section 11 is the cutover) and the shape of a
finished port (`hollis-labs/station` `docs/cutover-plan.md`).

Rule for the whole port: **code is a clean break, data is not.** Every record Cerberus holds today reaches the new
build, verified. Nothing is deleted; what is not migrated is left in place and named here.

The hard part is that Cerberus is the thing that deploys and supervises everything else, itself included. The
answer in this plan, section 7, is: **Cerberus never deploys itself.** The old instance supervises the new one as an
ordinary resource during the transition, the installer (`fleet-install`) places the binary, and supervision of the
other workloads is handed over by a flag, not by restarting them.

## 0. Decisions answered first

| Question (guide section 0) | Answer |
|---|---|
| Migrated data or fresh start? | **Migrated.** The app is in daily use. Note: the live SQLite file is **empty** (0 bytes); all live state is files under `~/.cerberus/` (section 5). The migration is a file-tree import, not a table copy. |
| Variant | `app-host`. One process serves the API, the console, MCP over HTTP and the supervision loop. Today's two units (daemon on a unix socket, `cerberus web` on a loopback port) become one. |
| Who issues identity? | Cerberus's own (`identity` module), owner created offline. Machine callers hold API keys with scopes. |
| Routes | Every `/api/` route has a scope (section 4.3). The outpost device protocol and the event-token endpoints authenticate their own callers and are open mounts that say so in code. |
| Background work | Supervision monitor, host probes, scheduler, event export, plugin processes. App code owns them; each stops on context cancel and is joined before modules stop (stop order in section 4.4). |
| UI | app-shell plus BOM-pinned kits; the app's nav and surfaces go in `ui/app.json`. Identity has no UI kit yet, so sign-in is app-owned until it lands (guide section 8). |
| Missing BOM pins | None known yet. Any found go through @substrate-manager as a Substrate PR; this cell does not edit `bom/bom.yaml`. |
| Where the port lives | **This repository, long-lived branch `substrate-port`** (created from `main` at `493a9f0`). Every port PR targets it. It reaches `main` only at cutover, run by the director. Section 10 covers keeping it in step with `main`. |

## 1. What changes for the owner

- **One password, typed once**, at the offline `cerberus identity setup` in the window. Never on the command line.
- **The CLI needs a key.** Today the CLI reaches the daemon over a unix socket with filesystem trust. The new CLI
  calls the loopback HTTP API with an API key read from a 0600 file (`cerberus-cli` key, created offline). Verbs and
  flags that skills and agents use do not change (section 6).
- **Approvals stay passkey-bound.** Approving a gated operation is a human act and keeps its WebAuthn ceremony in the
  console. The guide says strict-mode proofs are not part of a port path; for Cerberus they are the product's
  safety gate on live systems, so they are **ported as app code**, with their data. No API key and no local-trust
  MCP process can hold `cerberus.approve` (section 4.3).
- **One console port, new number.** The console moves from the old web unit to the new process on `127.0.0.1:4785`
  (4783 stays with the old web unit until it retires). The proxy entry, if one exists, changes in the window.
- **Workloads do not restart.** The 29 resource units are plain systemd user units with `Restart=always`; they do not
  depend on the daemon process. Handing supervision over restarts none of them (section 7).

## 2. What runs today (names only)

| Thing | Today |
|---|---|
| Daemon | user unit `com.hollis-labs.cerberus.service`, `ExecStart=~/.local/bin/cerberus daemon --foreground`, `Restart=always`; unix socket `~/.cerberus/cerberus.sock`, `daemon.lock`, pid file; event export via a device token under `~/.cerberus/events-tokens/` (drop-in sets the URL and token file). |
| Console | user unit `cerberus-web.service`, `~/.local/bin/cerberus-web web --listen 127.0.0.1:4783` (a copy of the same binary); login keys under `~/.cerberus/web/`. |
| Workloads | 29 units described "Cerberus resource ..." (`com.hollis-labs.cerberus.<project>.<id>.service` and some legacy names). All log into `~/.cerberus/apps/<project>/<id>/logs/`; artifact installs live under `~/.cerberus/apps/`. Several carry installer drop-ins (`20-fleet-exec.conf`) and `10-neutral-cwd.conf`. None uses the `cerberus run-secrets` shim today (checked); the renderer can add it, so the shim stays a contract (section 6). |
| Self-guard | The serving daemon refuses to deploy, apply, stop or remove itself (`internal/cerbapi/daemon_self_guard.go`). Self-upgrade today is a manual binary swap plus a supervisor restart. Old binaries are kept beside it as `cerberus.prev-<date>-<sha>`. |
| Source of the live binary | Not on a known commit: the operator's clone is 3 commits ahead of `origin/main` and 60 behind. **Rollback therefore uses a byte copy of the running binary (sha256 recorded), never a rebuild.** |

The full lists come from the prep inventories (section 13): runtime and units (CW-20261114-0088), data stores
(CW-20261114-0086), clients (CW-20261114-0090), console (CW-20261114-0091). Each lands as `docs/substrate-port/inventory-*.md`
and this plan is amended where they disagree with it.

## 3. Target layout

```
cmd/cerberus/            main.go, substrate.yaml, substrate_wiring.go (generated, committed)
internal/settings/       decodes the cerberus: config block; refuses bad values naming the key
internal/datadir/        resolves CERBERUS_DATA_DIR (default ~/.local/share/cerberus-substrate), AfterConfig hook
internal/legacyimport/   migrate-from (old ~/.cerberus tree -> new data dir) and export-legacy (rollback)
internal/supervise/      monitor, health probes, pause state, observe|supervise mode
internal/connector/      local (systemd, launchd, artifact), ssh, docker  (moved, not rewritten)
internal/registry/       descriptor discovery and validation (*.cerberus.yaml, schema unchanged)
internal/admin/          external connector service: dry-run, ack, redaction, audit (from cerbapi)
internal/approval/       passkey approvals bound to a plan
internal/audit/          hash-chained append-only log
internal/hosts/, scheduling/, outpost/, eventexport/, presence/, policy/, egress/, redact/
internal/api/            HTTP handlers, one file per area, every route with a scope
internal/mcp/            typed tools; names unchanged
internal/webui/          embeds all:dist (dist/.gitkeep kept)
pkg/                     connector, plugin, resource, secret, outpost: public contracts, unchanged
web/                     app-shell console
ui/app.json              app nav and surfaces
```

Config: defaults in `substrate.yaml` under `cerberus:`; the operator override is `~/.config/cerberus/substrate.yaml`
(`SUBSTRATE_CONFIG`). Data: `~/.local/share/cerberus-substrate/` (0700): `cerberus.db`, the signing key, imported
append-only logs, pause state. **Install root stays `~/.cerberus/apps`** (config key `cerberus.install_root`):
it holds the artifacts and log paths the 29 live units point at, and moving it means rewriting and restarting every
unit. It is adopted in place, not copied (section 5). Plugin binaries (`~/.cerberus/plugins`) are adopted in place
the same way.

## 4. Module mapping

### 4.1 Substrate replaces (deleted from the app)

| Old | New |
|---|---|
| `internal/config` global config load, env reads | `config` module + `internal/settings`; env table in `docs/conversion-notes.md` |
| ad hoc logging | `logging` |
| daemon socket server, `cerberus web` HTTP server, host check | `httphost` (loopback, Host check, `Stopping(r)` for SSE) |
| `internal/store/sqlite` own open | `database` module (`database.From(app)`), goose set via `UseMigrationFS`; reads on `Reader()`, writes in `WriteTx` |
| web login keys, sessions, `oauth` login parts | `identity` (sessions, API keys, offline bootstrap) + `auth` (scope table, `enforce`) |
| `internal/mcp` transport | `mcp` module: HTTP `/mcp` and stdio with `LocalTrust` and narrowed `LocalScopes` |
| CLI root, version, health | `cli` module root; the app adds its verbs |
| `daemon_lock`, pidguard for the HTTP side | one process per data dir via the database open lock plus the unit; `internal/daemon` reduced to what supervision needs |

`pluginhost`, `scheduler`, `policy`, `secrets`, `events`: each is evaluated in its slice against Cerberus's own
(4.2). Cerberus's plugin contract (`pkg/plugin`, `plugin.yaml`, capability vocabulary) is a published contract with
`cerberus-plugins` and third parties and does not change; if Substrate's `pluginhost` cannot serve it unchanged, the
app keeps its own host and files the gap.

### 4.2 Stays app code (moved, tests ported)

The supervision lane (`resource_runtime_service.go` semantics), the local connector (systemd, launchd, artifact, dev
session), the unit renderer, ssh and docker connectors, the admin lane (dry-run, `--ack`, redaction, generated MCP
tools), registry, approvals and passkeys, audit chain, policy rules and effect classes, egress labels, hosts and
probes, scheduling core, outpost controller, event export, presence, plugin host semantics, credential
issuance. These are what Cerberus is.

**Unit renderer parity is a hard requirement:** for every live descriptor the new renderer writes a unit
byte-identical to the one on disk (fixtures copied from the host, names only, no secret values). Without it, the
first apply after cutover would restart workloads.

### 4.3 Scopes

`cerberus.read`, `cerberus.operate` (start/stop/restart/apply/deploy), `cerberus.admin` (connector writes, plugin
install, host edits), `cerberus.approve` (approve a gated plan: console session with a passkey ceremony only),
`cerberus.outpost` (outpost mount), `authenticated`, `public` (`/healthz` only). MCP stdio runs with
`LocalTrust: true` and `LocalScopes: [cerberus.read, cerberus.operate, cerberus.admin]`. **Never
`cerberus.approve`**: agents run as the same user (guide section 5, item 9). A test asserts every route has a scope
and that no key or local principal can reach the approve routes.

### 4.4 Stop order

Stop accepting HTTP, then the scheduler, then host probes and event export (flush), then the supervision monitor
(it never stops workloads on exit), then plugin processes, then modules. Proved with a real process and SIGTERM.

## 5. Data migration

Everything is read from a **snapshot copy** of `~/.cerberus` (and `~/.local/share/cerberus`) taken in the window. The
live tree is never written by the migration. `cerberus migrate-from --old-home <snapshot>` writes the new data dir in
one run, refuses a non-empty target, and prints a report: per store, source sha256, record counts before and after,
digests and `source_unchanged: true`.

Treatments (the store inventory, CW-20261114-0086, completes and corrects this table):

| Store (old) | Treatment | Verified by |
|---|---|---|
| `audit/*.jsonl` (hash-chained, ~14k lines) | Copied verbatim into the data dir, read-only. The new log starts with a genesis record carrying the old chain's final hash. | old chain verifies end to end; new genesis links to it; line counts equal |
| `approvals/events.jsonl`, `approvals/passkeys/` | Imported into SQLite (events, passkey credentials). Originals also kept verbatim. | counts equal; each passkey can sign a test challenge in rehearsal |
| `hosts.yaml`, `hosts.history.jsonl`, `state/hosts.json`, `state/overview_snapshots.json` | Operator-authored YAML stays YAML in the new config dir; history and snapshots imported. | YAML byte-identical; counts equal |
| `config.yaml` (the live registry: which descriptors are registered), `registry.yaml`, `connector-config.yaml`, `events.yaml`, `policy/`, `outpost-recovery.cerberus.yaml` | Config: copied to the new config dir, keys mapped (table in conversion-notes); unknown keys are an error, not ignored. | `cerberus config` shows the effective values; diff reviewed |
| `connector-secrets.yaml`, `secrets/`, `credential-issuance/`, `outpost-tokens/`, `events-tokens/` | Copied file by file at 0600 through the `secrets` resolver's file backend; **values never printed**, verified by sha256 only. | sha256 per file equal |
| `runtime/applied.json` (per-resource applied digests), `plugin-connectors.json`, `plugin-data/` | Imported into SQLite (applied digests drive the "unchanged, no restart" decision, so they must be exact). | per-resource digest equal; a dry apply of every resource reports "unchanged" |
| `paused`, `paused.<id>` flags | Imported as pause state. **Carried exactly**: four resource pauses exist today. | set equal |
| `apps/` (~680 MB), `plugins/` (~130 MB) | **Adopted in place**, not copied (section 3). | live units unchanged; new build resolves the same paths |
| `~/.local/share/cerberus/` (`changes/`, `postgres-adoption/`, `review-evidence/`), the empty `cerberus.db` | Left in place; content imported if the inventory finds the code reads it. | inventory decides |
| `cerberus.log`, `logs/`, `backups/`, `transfers/`, `*.bak*`, `pids/`, `locks/`, `daemon.lock`, `cerberus.sock` | Not migrated, left in place (logs, backups and runtime files). | listed in the report as skipped, with reason |

Rehearsal: on a `.backup`/`cp -a` copy under the cell's scratch directory, with `HOME`, `XDG_*` and
`CERBERUS_DATA_DIR` pointed there, three runs; report counts, digests, the audit chain check, timing. Then the new
binary starts in `observe` mode on the copy with a fake supervisor backend and lists the same resources and states
the copy implies. Record it in section 10 of the cutover runbook (`docs/substrate-port/cutover-runbook.md`, a later
slice).

**Reverse path, so rollback loses nothing:** `cerberus export-legacy --since <handoff>` writes what the new build
recorded after handoff back into old formats: append-only records (audit, approvals, host history) as sidecar files
`*.post-cutover-<date>.jsonl` beside the old ones (the old chain is not rewritten), and mutable state (hosts, pauses,
applied digests, config edits) into the old files. Rehearsed old, new, old on the copy.

## 6. Clients

Preliminary; CW-20261114-0090 lists each with file:line, and the plan is amended from it.

| Caller | Interface | What changes |
|---|---|---|
| Agents and skills (`deploy`, `service-start`, `service-stop`, ad hoc CLI) | `cerberus` CLI | Nothing in their calls: verbs, flags and output used by callers stay. The CLI reads its key file. |
| Agent MCP configs, Tether gateway | `cerberus mcp` stdio, `cerberus_*` tool names | Tool names and shapes unchanged; stdio runs under local trust without `approve`. |
| Browser | console | New port 4785; sign-in through identity; passkeys migrated. |
| Tether (events) | event export with device token | Same token, same event shape; source is the new process. |
| Outposts (`cerberus-outpost`) | outpost protocol, tokens | Unchanged protocol; open mount verifying its own tokens; tokens migrated. |
| Hadron (`cerberus_op`) | MCP / API | Needs an API key with the scopes it uses; issued in the window. |
| `cerberus-plugins` and third-party plugins | `pkg/plugin`, `plugin.yaml`, capabilities | Unchanged (contract). |
| Go importers of `github.com/hollis-labs/cerberus/pkg/...` | module path | Unchanged. |
| Every repo's `*.cerberus.yaml` | descriptor schema `cerberus-project/v1` | Unchanged (contract). |
| `fleet-install` | writes drop-ins into Cerberus-generated units | Unchanged; renderer parity keeps its drop-ins valid. |
| `cerberus-presence` | separate binary | Inventory decides: ported as a subcommand or left as is. |

Changing any contract above would need the director's approval; this plan changes none.

## 7. Cutover: self-deploy sequencing

The new build is called **new** and the running one **old**. Phases are separate decisions; each has its own
rollback (section 8). Phases B onwards are run by the director with the owner, never by this cell.

**Ordering with other apps.** Tachyon is a Cerberus-supervised workload and cuts over first, through old. Cerberus's
window starts only after Tachyon's cutover is accepted. No other app cutover or Cerberus-driven deploy runs during
phases C to D.

**A. Prepare (cell, no host change).** CI builds `cerberus` on `substrate-port`. Rehearsal (section 5) passes. The
renderer parity test passes against fixtures of all 29 units. The runbook is written with exact commands.

**B. Pin old to a fixed binary (director; old daemon restarts once, workloads do not).** Today old runs from
`~/.local/bin/cerberus`, the same path the CLI is found at. Copy the running binary byte for byte to
`~/.local/libexec/cerberus-legacy/<sha256-prefix>/cerberus` (and `cerberus-web` likewise), record sha256, and point
the old daemon's and old web unit's `ExecStart` there with a drop-in. Restart old once and confirm it serves and
supervises. From here on, changing `~/.local/bin/cerberus` cannot change old.

**C. Shadow (director; no write pause).** Add the resource `cerberus-substrate` (project `cerberus`) to old's registry:
`os_service`, `run_from: artifact`, `command: [cerberus, serve]`, env `SUBSTRATE_CONFIG`, `CERBERUS_DATA_DIR`,
health `/healthz` on 4785. Old generates and owns its unit; `fleet-install deploy cerberus --run <id>` (dry run first)
places the binary under `~/.local/libexec/cerberus/<version>/` and writes its drop-in. The self-guard does not trip:
it is a different resource id and a different executable. New starts with an **empty data dir and `mode: observe`**:
- it reads descriptors and unit states and runs health probes;
- it refuses every mutation (apply, deploy, start, stop, restart, connector writes, schedules, event export) with a
  clear error naming observe mode;
- it writes only its own data dir.

Soak at least 24 h. Check: new's resource list and states equal old's (`cerberus resource list` and `cerberus resource status <id>` for every id, from each, diffed);
renderer parity against the live units; no writes under `~/.cerberus` by new (inotify or mtime check).

**D. Handoff window (director with owner; a write pause of minutes).**
1. Announce a freeze on Cerberus mutations to agents. Old keeps running.
2. Old global pause on: create the flag file `~/.cerberus/paused`, which old's monitor reads (`pausectl.IsGloballyPaused`; there is no CLI verb for it), so old auto-restarts nothing. Scheduler: the runtime inventory says how
   old's schedules are stopped without stopping old; if they cannot be, old's daemon is stopped for the rest of the
   window and started again in step 9 (old stays installed and startable in seconds). This is the one open point
   that gates the window.
3. Snapshot copy of `~/.cerberus` and `~/.local/share/cerberus` into a private 0700 directory; `integrity_check`
   where SQLite; sha256 manifest.
4. Stop new (through old: `cerberus resource stop cerberus-substrate`).
5. `cerberus migrate-from --old-home <snapshot>` into new's data dir (emptied first: observe mode wrote only probe
   state, which is regenerated). Read the report.
6. Offline identity: owner runs `cerberus identity setup --username <owner>`; then keys with `--out` (0600):
   `cerberus-cli` (read, operate, admin), `hadron` (its scopes), one per other machine caller from section 6.
7. Set `mode: supervise` in the override file. Start new through old (`cerberus resource apply cerberus-substrate`, then `resource status`). New now carries the pause state it imported,
   so resources paused before stay paused.
8. Point `~/.local/bin/cerberus` (and `cerberus-web` callers, if any remain) at the new binary. Old is unaffected
   (phase B).
9. Section 9 checks. Then lift the freeze. Old stays running **with its global pause on**: it no longer
   auto-restarts anything, but it still owns the `cerberus-substrate` unit and can stop, restart or roll back new.

**E. Soak (at least 7 days).** New supervises. Old is the supervisor of new. Daily: new's applied digests and pause
state are sane, no unexpected restarts, the audit chain verifies.

**F. Retire old (director).** Stop and disable old's daemon and web units (files kept). Re-home new under the
canonical unit `com.hollis-labs.cerberus.service` with a `fleet-install` drop-in, and remove the transitional
`cerberus-substrate` unit. That restarts new only. From then on **Cerberus is upgraded by `fleet-install`, never by
itself**; the self-guard stays. Merge `substrate-port` into `main` (director). `~/.cerberus` stays in place: the
install root and plugins live there (relocating them is follow-up work, one unit at a time).

## 8. Rollback

| From | Rollback |
|---|---|
| B | Remove the drop-in, restart old: back on `~/.local/bin/cerberus`, which is unchanged at this point. |
| C | `cerberus resource remove cerberus-substrate` on old; remove new's data dir. Nothing else changed. |
| D, before step 7 | Start nothing; remove `~/.cerberus/paused`; old is exactly as before (its files were only read). |
| D step 7 to F | Stop new through old. `cerberus export-legacy --since <handoff>` (from the new binary, on new's data dir), then repoint `~/.local/bin/cerberus` to the legacy copy, then remove `~/.cerberus/paused`. Workloads never restarted. Records new wrote are preserved (section 5, reverse path). |
| after F | Re-enable old's units from their files and the legacy binary; export-legacy as above; re-home is reversed by removing the drop-in. |

Old is never deleted before acceptance plus 30 days; the owner decides when.

## 9. Live verification

1. `/healthz` 200 on 4785. `GET /api/setup` answers `setup_required: false`; `POST /api/setup` 403.
2. No credential: 401. A key without the scope: 403. An unknown `Host`: 403. The `cerberus-cli` key on an approve route: 403.
3. Every resource's status from new equals the state old reported in the snapshot; pause set equal.
4. Dry apply of every resource reports "unchanged" (no unit rewrite, no restart).
5. One read and one write the owner chooses (for example pause and resume one non-critical resource), seen in the
   audit log, and the chain verifies across the genesis link.
6. One approval through the console with a migrated passkey.
7. An MCP tool call from an agent's stdio config; the approve tool is refused.
8. An event reaches Tether from the new process; one outpost reports in.
9. SIGTERM: new stops within its grace; no workload restarted (unit `ActiveEnterTimestamp` unchanged for all 29).

## 10. Branch policy

- Port PRs target `substrate-port`. Docs-only PRs about the port (this plan, inventories) may target `main`.
- **Keeping in step with `main`:** the lead opens a `sync/main-<date>` PR merging `main` into `substrate-port` weekly
  and within a day of any `main` merge that touches live behaviour. Reviewed like any PR. Where the fix touches code
  the port replaced, the merge takes the port's side and the lead files a task to carry the fix into the new code;
  the PR lists each one.
- `main` stays the live app's source until phase F. Live fixes go to `main` as today.

## 11. Slices (Torque tasks, each one PR into `substrate-port`)

1. Skeleton: manifest, wiring, entry point, settings, data dir, startup guard, CI (vet, race tests, lint, pins check,
   frontend build, `substrate scan-public`) running on PRs to `substrate-port`; `architecture.md` and
   `conversion-notes.md` started.
2. Store and registry: goose schema for derived state; descriptor loader with the v1 schema unchanged.
3. Supervision in observe mode: local connector moved, unit renderer with byte-parity fixtures, monitor, status.
4. Identity, scopes, API read routes, CLI over HTTP with key file; scope-coverage test.
5. Mutation lane and supervise mode: apply, deploy, start/stop/restart, artifacts, pause state, self-guard.
6. Audit chain, approvals and passkeys, policy, redaction, egress.
7. Admin lane: ssh, docker, external connector service, plugin host with `pkg/plugin` unchanged, secrets resolver.
8. Hosts and probes, scheduling, outpost mount, event export, presence.
9. MCP: HTTP and stdio, tool-name parity test, local scopes without approve.
10. `migrate-from` and `export-legacy` with the rehearsal on a copy (three runs, report).
11. Console on app-shell: pages per the console inventory, sign-in, approvals ceremony (GUI seat, two or three PRs).
12. Deploy and runbook: the `cerberus-substrate` descriptor, the `fleet-install` descriptor, `cutover-runbook.md`
    with exact commands for phases B to F.

Dependencies: 1 before all; 2 before 3; 3 and 4 before 5; 4 before 9 and 11; 5 to 9 before 10; all before 12.

## 12. Open points

- How old's scheduler is stopped without stopping old (gates phase D step 2; runtime inventory).
- Whether Substrate `pluginhost`, `scheduler`, `secrets` can carry Cerberus's contracts unchanged (slices 7 and 8;
  gaps filed in project Substrate, not patched here).
- Whether `httphost` can serve the API on a unix socket as well as TCP. Not needed by this plan (the CLI moves to
  HTTP with a key), recorded in case the inventory finds a direct socket caller.
- `cerberus-presence` and `~/.local/share/cerberus/*`: inventory decides.

## 13. Inventories feeding this plan

| Task | Seat | Output |
|---|---|---|
| CW-20261114-0088 | builder | `docs/substrate-port/inventory-runtime.md`: units, schedules, self-deploy, side-by-side collisions |
| CW-20261114-0086 | builder | `docs/substrate-port/inventory-data.md`: every store and table |
| CW-20261114-0090 | gui | `docs/substrate-port/inventory-clients.md`: every caller, cross-app contracts |
| CW-20261114-0091 | gui | `docs/substrate-port/inventory-gui.md`: console pages, auth, Substrate mapping |

# Substrate port plan: Cerberus

Status: **plan, revision 2, not started.** It has been amended from the review of #73 and from the four
inventories under `docs/substrate-port/`. No port code merges before the program director approves this plan.
Torque: CW-20261112-0099 (wave 3). Follows `hollis-labs/substrate` `docs/adopting-substrate.md` (section 11 is the
cutover) and the shape of a finished port (`hollis-labs/station` `docs/cutover-plan.md`).

The facts behind this plan, with their code references, are in the inventories:
- `inventory-runtime.md`: units, loops, self-deploy, and the 25 collision points between two daemons;
- `inventory-data.md`: 64 stores and their treatments;
- `inventory-clients.md`: every caller and the contracts with other apps;
- `inventory-gui.md`: the console.

Where this plan and an inventory disagree, the plan is wrong and gets fixed.

Rule for the whole port: **code is a clean break, data is not.** Every record reaches the new build, verified.
Nothing is deleted. Anything left in place or archived is named here.

**The hard part is that Cerberus deploys and supervises everything, itself included.** The answer here has four
parts, all in section 7:
- **Cerberus never deploys itself.** `fleet-install` places the new binary.
- **Old supervises new as an ordinary resource** until old is retired.
- **There is never more than one writer.** At the handoff, old enters a **standby mode** where it writes nothing
  and serves only the verbs that stop, start and inspect new.
- **Supervision of `os_service` workloads moves without restarting any of them.**

## 0. Decisions answered first

| Question (guide section 0) | Answer |
|---|---|
| Migrated data or fresh start? | **Migrated.** Live state has three parts: the workspace SQLite database `~/.local/share/cerberus/workspaces/default/main.db` (host monitoring, about 172k rows; the project, resource and schedule tables are empty), the file tree under `~/.cerberus/`, and three OS keyring entries. The `cerberus.db` beside the database is an unused 0-byte file. |
| Variant | `app-host`. One process serves the API, the console, MCP over HTTP and the supervision loops. Today's two units (the daemon on a unix socket, and `cerberus-web` on 4783) become one. |
| Identity | Cerberus's own (`identity` module), with the owner created offline. Machine callers hold API keys with scopes. |
| Routes | Every `/api/` route has a scope (section 4.3). The outpost device protocol authenticates its own callers and is an open mount that says so in code. |
| Background work | Resource monitor, host monitor and janitor, approval-expiry sweeper, overview snapshot recorder, plugin supervision, MCP plugin tool sync, event export (off). App code owns them; the stop order is in section 4.4. The scheduling core is wired but has never been started live; it is ported and stays off until it is turned on separately. |
| UI | app-shell plus BOM-pinned kits; the app's nav goes in `ui/app.json`. Sign-in is app-owned until identity has a UI kit. |
| Pins, Substrate gaps, identity questions | Through @substrate-manager. This cell does not edit `bom/bom.yaml` or Substrate. |
| Where the port lives | **This repository, long-lived branch `substrate-port`** (from `main` at `493a9f0`). Port PRs target it. Merges go through @substrate-manager. `main` stays the live app's source until phase F (section 10). |
| Live-app change needed | **Yes, one: the standby mode on `main` (section 7.1).** The director approves it with this plan. |

## 1. What changes for the owner

- **One password, typed once**, at the offline `cerberus identity setup` in the window.
- **The CLI uses an API key.** It calls the loopback HTTP API with a key read from a 0600 file instead of the unix
  socket. The verbs, flags and output that skills and agents use stay the same (section 6).
- **Approvals stay passkey-bound** and keep working with the same passkeys. The guide says strict-mode proofs are
  not part of a port; for Cerberus they are the product's safety gate on live systems, so they are ported as app
  code. The passkey registry is carried byte for byte with the audit chain that records its hash, so no 24-hour
  cool-down starts (section 5). No API key and no local-trust MCP process can hold `cerberus.approve`.
- **The console moves to `127.0.0.1:4786`.** 4783 stays with the old console until the window, and 4785 is the old
  build's `mcp-http` default, so both are avoided. The passkey RP ID is `localhost`, which is unaffected by the port
  change. The allowed origins are configured explicitly, because today they are derived from the old login-key file.
  If the console is reached through a proxy at `web.public_url`, the proxy entry changes in the window.
- **Restarts the owner will see** (section 7 names each one):
  - The 27 `os_service` units and the 8 native units are never restarted.
  - The plugin processes (7) and the one running dev session (`tachyon-kit-frontend`) restart at phase B, at the
    handoff and at the re-home in F. Each takes 30 s or less: plugins have restart backoff, and the dev session has
    `auto_restart`.

## 2. What runs today

Summary only; `inventory-runtime.md` has the tables.

| Thing | Today |
|---|---|
| Daemon | `com.hollis-labs.cerberus.service`, `~/.local/bin/cerberus daemon --foreground`, `Restart=always`, default `KillMode=control-group`. Unix socket, `daemon.lock`, pid file. Its cgroup holds 7 plugin processes and the dev-session tree. |
| Console | `cerberus-web.service` (written by hand), a byte copy of the same binary as `~/.local/bin/cerberus-web`, on `127.0.0.1:4783`. |
| Workloads | 27 Cerberus-written `os_service` units, 8 native units tracked as resources, 7 `dev_session` resources (1 running), 14 unsupervised handles (ssh and docker). 59 resources from 30 registered descriptors (`~/.cerberus/registry.yaml`). |
| Loops | See section 0. Event export is **off** (`export.enabled: false`). Host monitoring is on. |
| Outside units that read Cerberus state | `hub-backup.timer` archives `main.db`, `config.yaml`, `registry.yaml`, `plugin-connectors.json`, `audit/` and the unit dir. `events-credential-renew.timer` renews the events device token. |
| Self-deploy today | By hand: `mv` the new binary over `~/.local/bin/cerberus`, then `systemctl --user restart`. The `.prev-<date>-<sha>` copies are the rollback. The self-guard refuses a deploy over the daemon's own socket. |
| Live binary | `ce767e6` (`vcs.modified=false`), two commits behind `origin/main`. Rollback still uses a byte copy of the running binary, with its sha256 recorded. |

## 3. Target layout

```
cmd/cerberus/            main.go, substrate.yaml, substrate_wiring.go (generated, committed)
internal/settings/       decodes the cerberus: block; refuses bad values naming the key
internal/datadir/        CERBERUS_DATA_DIR (default ~/.local/share/cerberus-substrate), AfterConfig hook
internal/legacyimport/   migrate-from, export-legacy, verify
internal/supervise/      monitor, mode (observe|supervise), pause state, self identity
internal/connector/      local (systemd, launchd, artifact, dev session), ssh, docker
internal/registry/       descriptors (cerberus-project/v1, unchanged), registry index
internal/admin/          external connector service: dry-run, ack, redaction, generated tools
internal/approval/, presence/, audit/, policy/, egress/, redact/, brake/
internal/hosts/, scheduling/, outpost/, eventexport/, credentialbroker/, pluginhost/
internal/api/            HTTP handlers; every route has a scope
internal/mcp/            typed tools; names unchanged
internal/webui/          embeds all:dist (dist/.gitkeep kept)
pkg/                     connector, plugin, resource, secret, outpost: public contracts, unchanged
web/, ui/app.json        the console on app-shell
```

**Two roots.**
- **The data dir** `~/.local/share/cerberus-substrate/` (0700) holds what new owns and writes:
  - SQLite (host monitoring, pause state, applied digests, credential-issuance receipts);
  - the hash-chained logs (audit, approvals, host history), kept as JSONL files in their current format;
  - the passkey registry files;
  - state snapshots;
  - the signing key.
- **The adopted root** `~/.cerberus` (config key `cerberus.adopt_root`) holds what new reads and writes **in place**
  because live units, other tools or recorded absolute paths point into it:
  - `apps/`, the install root of the live units;
  - `plugins/`, `plugin-data/`, `plugin-cache/` (plugin bundles and runtime roots, whose digest paths are recorded);
  - `outpost-tokens/` (named by absolute path in `hosts.yaml`);
  - `events-tokens/` (written by the renew timer);
  - `secrets/` and `credential-issuance/secrets/issued/` (whose consumers hold the paths);
  - `transfers/`.

  New never reads `~/.cerberus/paused*`, `pids/`, `locks/`, `daemon.lock` or the socket (a test asserts this).

Config: the operator override is `~/.config/cerberus/substrate.yaml` (`SUBSTRATE_CONFIG`).

## 4. Module mapping

### 4.1 Substrate replaces (deleted from the app)

| Old | New |
|---|---|
| global config load, env reads, `CERBERUS_DB_PATH`/`CERBERUS_WORKSPACE` | `config` module + `internal/settings`; env table in `docs/conversion-notes.md` |
| ad hoc logging, `cerberus.log` (unrotated, 239 MB) | `logging` |
| socket server, `cerberus web` server, host check | `httphost` on loopback, Host check, `Stopping(r)` for streams |
| `internal/store/sqlite` | `database` module, goose set, `Reader()`/`WriteTx` discipline |
| web login keys and sessions | `identity` + `auth` (`enforce`) |
| `internal/mcp` transport | `mcp` module: HTTP `/mcp`; stdio with `LocalTrust` and narrowed `LocalScopes` |
| CLI root | `cli` module root; the app adds its verbs |
| `daemon.lock`, pid file, stray sweep, `procident` | unit plus the database open lock; supervision identifies itself by unit (4.2) |

`pluginhost`, `scheduler`, `secrets`, `policy`, `events`: each is evaluated in its slice. The rule: **a published
contract does not change to fit a module.** Today the plugin contract and the keyring service and account names
are published contracts. If a module cannot carry one, the app keeps its own code and the gap is filed in project
Substrate through @substrate-manager.

### 4.2 Stays app code (moved, tests ported)

The supervision lane, the local connector, the unit renderer, the ssh and docker connectors, the admin lane,
registry, approvals and presence, audit, policy, egress, brakes, hosts and probes, scheduling core, outpost
controller, event export, credential broker, plugin host semantics.

Hard requirements carried into the slices:
- **Unit renderer parity.** For every live descriptor, the new renderer writes a unit byte-identical to the file on
  disk (fixtures from the host, names only). Otherwise the first apply after cutover restarts workloads.
- **Self identity.** New knows which resource it is from config (`cerberus.self_resource_id`), checked at start
  against its own systemd unit (`/proc/self/cgroup`). It refuses deploy, apply, sync, reload, stop or remove on
  that resource through its own API. It does not rely on `XPC_SERVICE_NAME` (macOS only) or the artifact path
  (`fleet-install` places the binary elsewhere). A test covers the Linux case.
- **Hash-chained log formats unchanged.** Audit and approvals records are written in the existing format, continuing
  the existing chain, so old can verify across them (rollback, section 8).
- **Mode.** `observe` reads descriptors, unit states and resource status and serves reads. It starts no plugin, no
  host probe, no sweeper, no scheduler and no export, and refuses every mutation with an error naming observe mode.
  `supervise` is the full app.

### 4.3 Scopes

| Scope | Covers |
|---|---|
| `cerberus.read` | reads |
| `cerberus.operate` | start, stop, reload, apply, deploy |
| `cerberus.admin` | connector writes, plugins, hosts, policy |
| `cerberus.approve` | approving a gated plan: a console session plus a passkey ceremony only |
| `cerberus.outpost` | the outpost mount |
| `public` | `/healthz` |

MCP stdio runs with `LocalTrust: true` and `LocalScopes: [cerberus.read, cerberus.operate, cerberus.admin]`, **never
`cerberus.approve`**. Tests assert that every route has a scope and that no key or local principal reaches approve.

### 4.4 Stop order

1. Stop accepting HTTP.
2. Stop the scheduler (if on).
3. Stop the host monitor and janitor, the sweeper, the snapshot recorder and event export, flushing each.
4. Stop the resource monitor. It never stops an `os_service` workload on exit.
5. Stop plugin processes.
6. Stop the modules.

This is proved with a real process and SIGTERM.

## 5. Data migration

`cerberus migrate-from --old-home <snapshot-of-~/.cerberus> --old-db <backup-of-main.db>`:
- writes the data dir in one run and refuses a non-empty target;
- reads only the snapshot and the backup, never the live tree;
- prints a report: per store, source sha256, record counts before and after, digests, chain verification, and
  `source_unchanged: true`.

The snapshot is taken with old in standby (section 7, D3), when nothing writes. `main.db` is copied with
`sqlite3 main.db ".backup <copy>"` (it is in WAL mode), never `cp`.

Treatments, using the vocabulary of `inventory-data.md`. Every row of that inventory falls into one of these lines.

| Store | Treatment | Verified by |
|---|---|---|
| `main.db`: `host_samples`, `host_status`, `host_sample_rollups`, `host_rollup_state`, `host_events` (+ the `sqlite_sequence` value) | **transform** into the new schema, every row, ids kept | `integrity_check`; per-table counts; `EXCEPT` both ways 0 on the copy |
| `main.db`: `projects`, `resources`, every `cerberus_schedule_*` and `gosched_*` table | **assert 0 rows**, then regenerate (new schema, empty). If any has rows on the day, the window stops: it becomes a transform first. | counts printed |
| `main.db`: `schema_migrations` | regenerate (goose); the import refuses any old version set other than {1,2,3,5,6,7,8} | report |
| `audit/*.jsonl` + `audit/.digest_key` (secret, 0600) | **copy** byte for byte into the data dir; new continues the chain from its last head in the same format | `audit verify` on the copy before and after; sha256 of the key |
| keyring `cerberus-audit-anchor` (account = audit dir path) | **transform**: write the same head under the new dir's path; the old entry stays | `audit verify` against the old anchor (old path) and the new one |
| `approvals/events.jsonl` | **copy** byte for byte; new continues the chain | chain verify; line count |
| `approvals/passkeys/keys.json`, `key-counters.json` | **copy** byte for byte (no re-serialisation; the registry hash recorded in audit must match, or a 24 h cool-down starts) | sha256 equal; one approval on the rehearsal copy verifies with no cool-down |
| `hosts.history.jsonl` (+ rotations), `state/hosts.json`, `state/overview_snapshots.json` | **copy** | sha256 |
| `runtime/applied.json` | **transform** into SQLite, digest for digest | per-resource digest equal; observe mode reports no resource as changed |
| `paused`, `paused.<id>` (4) | **transform** into pause state (operator intent) | set equal |
| `credential-issuance/<id>/*.json` (5) | **transform** (receipts) | counts |
| `policy/` (`applied.yaml`, `applied.sha256`, `posture.yaml`, `enforcement.yaml`, `providers/*`) | **copy** into the config dir; `applied.sha256` is checked against `applied.yaml` before and after | sha256; `policy show` equal on both |
| `config.yaml`, `events.yaml`, `connector-config.yaml` | **transform** to config keys (table in conversion-notes; unknown key = error). `export.enabled` stays `false`. | `cerberus config` reviewed against the old values |
| `registry.yaml` | **copy**; the descriptors it lists stay in place (they belong to their apps) | 30 entries, 59 resources listed by observe mode |
| `hosts.yaml` | **copy**; `token_file` paths keep pointing into the adopted root | `hosts list` equal |
| `connector-secrets.yaml` (secret refs) | **copy** at 0600 | sha256 |
| keyring `cerberus` service: connector and plugin credentials, `cerberus-oauth/issuer-ed25519` | **in place**: same service and account names (a contract) | one credentialed health op succeeds |
| `apps/`, `plugins/`, `plugin-data/` (204 dirs), `plugin-cache/` (204 dirs), `outpost-tokens/`, `events-tokens/`, `secrets/`, `credential-issuance/secrets/issued/`, `transfers/`, `plugin-connectors.json` | **in place** under the adopted root (section 3). `plugin-connectors.json` is read and written in place, because it records the bundle digest paths. Exactly one daemon runs plugins at any time. | plugins load with the same digests |
| `pids/<id>.{pid,meta}` | not copied. Running dev sessions are stopped under old and started under new in the window (D7), so there is nothing to adopt. | session status after D7 |
| `brakes/`, `oauth/tokens.jsonl`, `events/`, `alerts/` | absent today; the window re-checks. **copy** if present. | listed in the report |
| `web/login-*.key` | not migrated: replaced by identity sessions | — |
| `cerberus.log`, `logs/`, `backups/`, `*.bak*`, `~/.local/share/cerberus/{changes,postgres-adoption,review-evidence}`, the 0-byte `cerberus.db` | **archive**: left where they are and never deleted; the owner decides | listed in the report |
| `~/.local/state/cerberus/` (backup passphrases, postgres passwords) | **in place**; not Cerberus state, but must never be lost | listed |
| socket, `cerberus.pid`, `daemon.lock`, `locks/`, `*.lock` | runtime; not migrated | — |

**Rehearsal (cell, scratch directory, never the live tree).**
1. Take a `.backup` of `main.db` and a `cp -a` of `~/.cerberus` (old running; the rehearsal does not need standby).
2. Point `HOME`, `XDG_*` and `CERBERUS_DATA_DIR` at the scratch directory. Use a scratch keyring (`dbus-run-session`
   with its own gnome-keyring), because the anchor lives in the keyring.
3. Run `migrate-from` three times. Record counts, digests, both chain checks and timing.
4. Start new in `observe` mode on the copy with a fake supervisor backend. It must list the same 59 resources, the
   same pause set and no "changed" resource.
5. Verify one approval with a copied passkey and no cool-down.
6. Run `export-legacy` back onto a second copy, then run the old binary's `audit verify` there.

**Reverse path (rollback).** `cerberus export-legacy --since <handoff>`:
- refuses to run unless every old file it would touch still has the sha256 in the D3 snapshot manifest (fail
  closed);
- appends the audit and approvals records written after the handoff to the old files (the same format and chain,
  so old verifies them) and moves the anchor back;
- copies host history, the pause set, applied digests and host-monitor rows newer than the snapshot back into
  `main.db` and the old files;
- leaves the new data dir intact.

## 6. Clients

From `inventory-clients.md` (all callers by role). What changes for each:

| Caller | Interface | Change |
|---|---|---|
| Agents and skills (`deploy`, `service-start`, `service-stop`, ad hoc) | `cerberus` CLI on `PATH` | The verbs, flags and output they use stay the same; the CLI reads its key file. At D8 `~/.local/bin/cerberus` becomes new. |
| Long-lived `cerberus mcp` stdio children (agent harnesses) and the Tether gateway's cerberus entries | stdio MCP, `cerberus_*` tool names | Tool names and shapes stay the same. Children started before D8 hold the old binary and dial old's socket; in standby old answers each call with an error naming the new CLI. **D9 restarts the gateway's entries**, and harnesses respawn the rest. The MCP server id stays `cerberus`: renaming it would disable the gateway's authority gate for it. |
| Browser | console | New port 4786; sign-in through identity; passkeys carried; proxy entry (if any) changed in D. |
| Outposts | outpost protocol, tokens in the adopted root | Unchanged; the open mount verifies its own tokens. |
| Hadron (`cerberus_op`) and other API or MCP machine callers named in the inventory | MCP / API | Each gets an API key with its scopes in D6. |
| `cerberus-plugins` and third-party plugins | `pkg/plugin`, `plugin.yaml`, capabilities | Unchanged (contract). |
| Go importers of `github.com/hollis-labs/cerberus/pkg/...` | module path | Unchanged (contract). |
| Every repo's `*.cerberus.yaml` | `cerberus-project/v1` | Unchanged (contract). |
| `fleet-install` | drop-ins in Cerberus-written units | Unchanged; renderer parity keeps them valid. |
| `hub-backup.timer` | reads `main.db`, `config.yaml`, `registry.yaml`, `plugin-connectors.json`, `audit/` | **Its path list gains the new data dir and config dir (D, director: its owner).** The old paths stay listed until F. |
| `events-credential-renew.timer` | writes `events-tokens/` | Unchanged (adopted root). Export is off; turning it on later is a separate change with Tether's owner. |
| Processes old spawns that call `cerberus` through `PATH` | CLI | Old's unit `PATH` is pinned in phase B to put the legacy binary first. |
| `cerberus-presence` | separate binary (old build) | Left as is; it reads no store this plan moves (inventory). |

No contract in this list changes. Changing one would need the director.

## 7. Cutover: self-deploy sequencing

Old means the running build, new the port. Phases are separate decisions. Phases B, D and F change the live host
and are run by the director with the owner, never by this cell.

**Ordering.** Tachyon cuts over first, through old. Cerberus's phase D starts only after Tachyon is accepted. No
other app cutover or Cerberus-driven deploy runs from D until new is accepted at the end of E.

**Paths.**
- `$LEGACY`: `~/.local/libexec/cerberus-legacy/<sha256-prefix>/cerberus`, the pinned old binary (phase B).
- `$NEW`: the `fleet-install` link for new.

**Every "through old" command uses `$LEGACY` explicitly**, never `cerberus` on `PATH`, because from D8 onwards that
is new.

### 7.1 The standby mode (a live-app change on `main`, slice 0)

A small change to the old code, released and deployed like any live fix. When the flag file `~/.cerberus/standby`
exists, at start or when it appears (polled each second), old:
- stops and does not start plugin processes;
- stops the host monitor and janitor, the approval-expiry sweeper, the snapshot recorder, the resource monitor and
  event export;
- refuses every socket request except reads and `resource status|start|stop|reload` on the one resource named in the
  flag file (`cerberus-substrate`). The refusal names the new CLI.
- writes nothing to `~/.cerberus` or `main.db` except its own audit lines for those allowed verbs.

**Why the audit carve-out is safe.** Those audit lines exist only in old's chain. Old writes them only between the
snapshot and the handoff, or during a rollback, and `export-legacy` then appends new's records after them. Neither
case forks the chain, because new does not write until D7.

Removing the flag returns old to normal at once. Tests prove each loop stops, and that the allowed set is exactly the
one above.

### 7.2 Phases

**A. Prepare (cell, no host change).**
- CI builds new on `substrate-port`.
- The section 5 rehearsal passes.
- The renderer parity fixtures for all 27 Cerberus-written units pass.
- The standby mode (slice 0) is merged to `main` through @substrate-manager.
- `docs/substrate-port/cutover-runbook.md` lists every command below with its expected output.

**B. Pin old and give it standby (director).** Old restarts once. The plugins and the dev session restart; no unit
does.
1. Copy the running `~/.local/bin/cerberus` and `cerberus-web` byte for byte as rollback copies, with sha256.
2. Build the release with slice 0 from `main` and place it at `$LEGACY` (and the web binary beside it).
3. Add a drop-in to old's daemon and web units with:
   - `ExecStart=` on `$LEGACY`;
   - `Environment=PATH=` with `$LEGACY`'s directory first.
4. `systemctl --user daemon-reload` and restart both. Old now serves from `$LEGACY`.
5. Check: `$LEGACY daemon status` (origin systemd), `$LEGACY resource list` (59 resources), plugins loaded,
   `tachyon-kit-frontend` back within 30 s.

**Rollback:** remove the drop-ins and restart; old runs the untouched `~/.local/bin/cerberus` again.

**C. Shadow (director; no pause).**
1. Register a descriptor in old's registry for the resource `cerberus-substrate`, project `cerberus`:
   - `os_service`, systemd user;
   - **no build strategy and not `run_from: artifact`**: `command: [$NEW, serve]`;
   - env `SUBSTRATE_CONFIG`, `CERBERUS_DATA_DIR`;
   - health `/healthz` on 4786.

   `fleet-install` alone places and links the binary (dry run first). Old never builds or deploys this resource.
   It only applies, starts, stops and reports on it.
2. New starts with an empty data dir in `mode: observe` and `cerberus.self_resource_id: cerberus-substrate`.

Soak for at least 24 h. Checks:
- new's resource list and statuses equal old's (diffed);
- renderer parity against the live unit files;
- nothing under `~/.cerberus` or in `main.db` written by new (mtime and inode check);
- no plugin process with new as its parent.

**D. Handoff window (director with owner; a write pause of minutes).**
1. Announce a freeze on Cerberus mutations. Re-check the scheduler and brake/oauth/event stores (section 5): any
   surprise stops the window.
2. Stop `cerberus-web.service` (old console). Then `echo cerberus-substrate > ~/.cerberus/standby`. Wait until old
   reports standby (`$LEGACY daemon status`); its plugins are stopped.
3. **Snapshot.**
   - `sqlite3 main.db ".backup <dir>/main.db"`, then `integrity_check`.
   - `cp -a ~/.cerberus <dir>/cerberus-home`, excluding `apps/` and `plugins*/`, which are adopted.
   - Write a sha256 manifest of every old file that `export-legacy` could touch.
   - All of it goes into a private 0700 directory.
4. `$LEGACY resource stop cerberus-substrate`. Move new's observe-mode data dir aside, not deleted.
5. `$NEW migrate-from --old-home <dir>/cerberus-home --old-db <dir>/main.db`. Read the report: counts, chains,
   anchor, `source_unchanged`.
6. Offline identity:
   - The owner runs `$NEW identity setup --username <owner>` at a terminal.
   - Then create keys with `--out` (0600): `cerberus-cli` (read, operate, admin), `hadron`, and one per machine
     caller in section 6.
7. Hand over.
   - Set `mode: supervise` in the override file, then `$LEGACY resource start cerberus-substrate` and
     `$LEGACY resource status cerberus-substrate`.
   - New loads the plugins and starts the host monitor.
   - For each `dev_session` that old was running (today only `tachyon-kit-frontend`): old already stopped it when it
     entered standby, so `$NEW resource start <id>`.
8. Point `~/.local/bin/cerberus` at `$NEW` (atomic rename). Old is unaffected: it runs from `$LEGACY`.
9. Section 9 checks, including a restart of the Tether gateway's cerberus entries. The director updates the proxy
   entry (if any) and `hub-backup`'s path list. Then lift the freeze.

Old stays running in standby. It owns the `cerberus-substrate` unit and can stop, start or roll back new. It writes
nothing else.

**E. Soak (at least 7 days).** New supervises. Daily checks:
- both audit chains verify;
- pause state is as expected;
- no unexpected restarts (`ActiveEnterTimestamp` of the 35 units unchanged unless the owner restarted one);
- host monitoring rows grow on new only.

**F. Retire old and re-home new (director).**
1. Stop and disable old's daemon and web units (unit files and `$LEGACY` kept).
2. Re-home new on the canonical unit `com.hollis-labs.cerberus.service`: a `fleet-install` drop-in with `ExecStart`
   on `$NEW`, `cerberus.self_resource_id` set to the daemon. Remove the transitional `cerberus-substrate` unit and
   its descriptor.

   This restarts new only; its plugins and dev sessions come back within 30 s. From here on, **Cerberus is upgraded
   by `fleet-install`, never by itself**, and the self-guard stays.
3. Merge `substrate-port` into `main` (director, through @substrate-manager).

`~/.cerberus` stays as the adopted root. Moving `apps/` and the plugin roots out of it is follow-up work, one unit at a
time.

## 8. Rollback

| From | Rollback |
|---|---|
| B | Remove old's drop-ins and restart: back on the untouched `~/.local/bin/cerberus`. |
| C | `$LEGACY resource stop cerberus-substrate`, remove the descriptor; move new's data dir aside. |
| D, before step 7 | `rm ~/.cerberus/standby`; start `cerberus-web`. Old is as it was (its files were only read). |
| D step 7 to the end of E | 1. `$LEGACY resource stop cerberus-substrate` (new stops its plugins and dev sessions). 2. `$NEW export-legacy --since <handoff>`, which fails closed if any old file changed since D3. 3. Repoint `~/.local/bin/cerberus` to the legacy binary. 4. `rm ~/.cerberus/standby` (old starts its loops and plugins again). 5. Start `cerberus-web`. 6. Start dev sessions under old. 7. Verify with `$LEGACY audit verify`. No `os_service` workload restarts. |
| after F | Re-enable old's units (files kept), stop new, then the same steps from step 2 above. Re-home is undone by removing the drop-in. |

Old's files, `$LEGACY` and the rollback copies are kept until acceptance plus 30 days; the owner decides when to
delete them.

## 9. Live verification (D9, and again after F)

1. `/healthz` is 200 on 4786. `GET /api/setup` answers `setup_required: false`; `POST /api/setup` answers 403.
2. Refusals:
   - no credential: 401;
   - a key without the scope: 403;
   - an unknown `Host`: 403;
   - the `cerberus-cli` key on an approve route: 403;
   - a call to old's socket from a pre-D8 MCP child: the standby error naming the new CLI.
3. New's resource list and statuses equal the D3 snapshot; pause set equal; observe-equivalent dry run reports no
   resource as changed (no unit rewritten, no restart).
4. One write the owner chooses (pause and resume a non-critical resource). It is recorded in audit, and `audit
   verify` passes across the old head into new's records against the new anchor.
5. One approval through the console with a carried passkey: no cool-down.
6. One MCP tool call through the restarted Tether gateway entry; the approve tool is refused under local trust.
7. One outpost reports in; one credentialed connector health op succeeds (keyring in place).
8. Plugins: 7 processes, parent new, none parented by old. Dev session `tachyon-kit-frontend` running under new.
9. SIGTERM to new (through old): new stops within its grace; no workload unit restarted.

## 10. Branch policy

- Port PRs target `substrate-port`. Docs-only PRs about the port (this plan, the inventories) and slice 0 (the
  standby mode, a live fix) target `main`.
- Merges go through @substrate-manager after reviewer approval at the head SHA and green CI. The lead sends the PR
  URL and the reviewed SHA.
- **Keeping in step with `main`.** The lead opens a `sync/main-<date>` PR merging `main` into `substrate-port`:
  - weekly, and within a day of any `main` merge that changes live behaviour;
  - reviewed like any PR;
  - where a fix touches code the port replaced, the merge takes the port's side and the lead files a task to carry
    the fix into the new code. The PR lists each one.

## 11. Slices (Torque tasks; each one PR)

| # | Base | Slice |
|---|---|---|
| 0 | `main` | Standby mode in old (section 7.1), with tests. |
| 1 | `substrate-port` | Skeleton: manifest, wiring, entry point, settings, data dir, startup guard, CI. |
| 2 | `substrate-port` | Store schema (goose) and registry (v1 descriptors, `registry.yaml` index). |
| 3 | `substrate-port` | Supervision in observe mode: local connector, renderer with byte-parity fixtures, status, self identity. |
| 4 | `substrate-port` | Identity, scopes, read API, CLI over HTTP with a key file; scope-coverage test. |
| 5 | `substrate-port` | Supervise mode: apply, deploy, start, stop, reload, artifacts, pause state, dev sessions, self-guard. |
| 6 | `substrate-port` | Audit (same format, anchor), approvals and presence (byte-exact registry), policy, redaction, egress, brakes. |
| 7 | `substrate-port` | Admin lane: ssh, docker, external connector service, plugin host (`pkg/plugin` unchanged, adopted roots), secrets (keyring names unchanged), credential broker. |
| 8 | `substrate-port` | Hosts and probes, janitor, scheduling core (off), outpost mount, event export (off), snapshot recorder. |
| 9 | `substrate-port` | MCP: HTTP and stdio, tool-name parity test, local scopes without approve. |
| 10 | `substrate-port` | `migrate-from`, `export-legacy`, verify, and the rehearsal (three runs, report). |
| 11a to c | `substrate-port` | Console on app-shell: (a) contract and scaffold, (b) pages, (c) sign-in and the approval ceremony. |
| 12 | `substrate-port` | Deploy: `cerberus-substrate` descriptor, `fleet-install` descriptor, `cutover-runbook.md`. |

Dependencies:
- slice 1 before every `substrate-port` slice;
- 2 before 3;
- 3 and 4 before 5;
- 4 before 9 and 11b;
- 5 to 9 before 10;
- 0 and 10 before 12.

## 12. Open points

- Can Substrate `pluginhost`, `scheduler` and `secrets` carry Cerberus's contracts unchanged? Answered in slices 7
  and 8; gaps go to project Substrate through @substrate-manager.
- The owner of `hub-backup` and of the console proxy (if any) is named in the runbook before D.
- `transfers/` (operator payloads) is adopted in place. Whether to move it later is the owner's call.

## 13. Inventories

| Task | Output |
|---|---|
| CW-20261114-0088 | `docs/substrate-port/inventory-runtime.md` (#74) |
| CW-20261114-0086 | `docs/substrate-port/inventory-data.md` (#77) |
| CW-20261114-0090 | `docs/substrate-port/inventory-clients.md` (#75) |
| CW-20261114-0091 | `docs/substrate-port/inventory-gui.md` (#76) |

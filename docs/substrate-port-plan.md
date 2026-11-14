# Substrate port plan: Cerberus

Status: **plan, revision 3, not started.** It has been amended from two reviews of #73 and from the four inventories
under `docs/substrate-port/`. No port code merges before the program director approves this plan. Torque:
CW-20261112-0099 (wave 3). Follows `hollis-labs/substrate` `docs/adopting-substrate.md` (section 11 is the cutover)
and the shape of a finished port (`hollis-labs/station` `docs/cutover-plan.md`).

This repository is public. The plan therefore describes the operator host by role and keeps code-derived facts and
contracts only. **Live observations** (counts, unit and drop-in names, paths, ports, outside jobs) are in the Tesseract
workspace item `01M7DX516HNDA31YMYKJ1Q06NP` (`project/substrate-migration/workspace/cerberus`, key `cerberus-plan-live-facts`),
referred to below as **the live-facts item**. They are re-taken at rehearsal and in the window.

The inventories (`inventory-runtime.md`, `inventory-data.md`, `inventory-clients.md`, `inventory-gui.md`) carry the
code references. Where this plan and an inventory disagree, the plan is wrong and gets fixed.

Rule for the whole port: **code is a clean break, data is not.** Every record reaches the new build, verified.
Nothing is deleted. Anything left in place or archived is named here.

**The hard part is that Cerberus deploys and supervises everything, itself included.** The answer here (section 7)
has four parts:
- **Cerberus never deploys itself.** `fleet-install` places the new binary.
- **Old supervises new as an ordinary resource** until old is retired.
- **There is never more than one writer.** At the handoff, old enters a **standby mode**. In standby it does not
  write to its state; it serves the verbs that stop, start and inspect new, and records them in a separate chain.
- **Supervision of `os_service` workloads moves without restarting any of them.**

## 0. Decisions answered first

| Question (guide section 0) | Answer |
|---|---|
| Migrated data or fresh start? | **Migrated.** Live state has three parts: the workspace SQLite database `<XDG_DATA_HOME>/cerberus/workspaces/default/main.db` (`internal/config/layout.go`), the file tree under `~/.cerberus/`, and three OS keyring entries. Only the host-monitoring tables hold rows; the project, resource and schedule tables are empty (the live-facts item has the counts). |
| Variant | `app-host`. One process serves the API, the console, MCP over HTTP and the supervision loops. Today's daemon (unix socket) and console process (`cerberus web`) become one. |
| Identity | Cerberus's own (`identity` module), with the owner created offline. Machine callers hold API keys with scopes. |
| Routes | Every `/api/` route has a scope (section 4.3). The outpost device protocol authenticates its own callers and is an open mount that says so in code. |
| Background work | Resource monitor, host monitor and janitor, approval-expiry sweeper, overview snapshot recorder, plugin supervision, MCP plugin tool sync, event export (off live). App code owns them; the stop order is in section 4.4. The scheduling core is wired but has never been started; it is ported and stays off until it is turned on separately. |
| UI | app-shell plus BOM-pinned kits; the app's nav goes in `ui/app.json`. Sign-in is app-owned until identity has a UI kit. |
| Pins, Substrate gaps, identity questions | Through @substrate-manager. This cell does not edit `bom/bom.yaml` or Substrate. `libs/mesh/eventbus` is being added to the BOM. `libs/security/webauthn` is pinned only if decision 0.1 keeps passkey approvals. |
| Where the port lives | **This repository, long-lived branch `substrate-port`** (from `main` at `493a9f0`). Port PRs target it. Merges go through @substrate-manager. `main` stays the live app's source until phase F (section 10). |
| Live-app change needed | **Yes, one: the standby mode on `main` (section 7.1).** The director approves it with this plan. |
| Owner decision | **Keep passkey approvals (0.1)?** Recommended: keep. |
| Changes in other apps | **Two socket clients must move to the new API before the handoff** (section 6). Their owners and scheduling are the director's. |

### 0.1 Decision for the owner: keep passkey approvals?

The adoption guide (section 5, item 8) says strict-mode proofs, passkeys and signed approvals included, are not part
of a port: they are dropped and recorded as a follow-up. Tachyon dropped its WebAuthn route under that rule. Keeping
them in Cerberus is an exception, which the owner grants through the director. @substrate-manager pins
`libs/security/webauthn` the same day if it is granted.

- **Who uses it today.** The owner, alone, approves gated admin-lane operations (destructive or high-effect
  connector writes) out of band with a passkey. The approvals log has a few dozen entries since September (the
  live-facts item has the count). Agents request approvals; they cannot give them.
- **What it protects against.** Agents run as the owner's OS user on the same host. A passkey assertion needs user
  presence on an authenticator, which no process of that user can produce. That is why `cerberus.approve` is
  unreachable from any key or local-trust MCP process.
- **If dropped**, the owner approves from a signed-in identity session in the console, as in Tangent and Tachyon:
  - approval stays human and bound to the plan hash;
  - the console session's password is the only factor, and a same-user agent that ever learns it, or holds a
    browser session, could approve;
  - the passkey files and the approvals chain are still migrated and kept (data is not dropped), but no longer
    verified;
  - slices 6 and 11c get smaller; the presence and cool-down code is not ported.
- **The cost of keeping it:**
  - one BOM pin;
  - `internal/presence` ported as app code (registry, counters, cool-down, the ceremony) in slices 6 and 11c;
  - the allowed-origins config and the RP-ID rehearsal assertion;
  - Cerberus carries a strict-mode path alone until Substrate has one, and that is recorded as a Substrate
    follow-up.
- **Recommendation: keep.** Cerberus is the one app whose approvals gate writes to live infrastructure, and the
  same-user-agent threat is exactly what the passkey closes.

Until it is decided, the skeleton builds without `internal/presence`, and slice 11c waits.

## 1. What changes for the owner

- **One password, typed once**, at the offline `cerberus identity setup` in the window.
- **The CLI uses an API key.** It calls the loopback HTTP API with a key read from a 0600 file instead of the unix
  socket. The verbs, flags and output that skills and agents use stay the same.
- **Approvals: passkey-bound if decision 0.1 keeps them** (the recommendation). The passkey registry is then carried
  byte for byte with the audit chain that records its hash, so no 24-hour cool-down starts. Either way, no API key and
  no local-trust MCP process can hold `cerberus.approve`.
- **The console moves to `127.0.0.1:4786`.** The old console's default is 4783, and the old build's `mcp-http`
  default is 4785, so both are avoided. **A passkey is bound to the hostname of the origin it was enrolled on (its RP
  ID)**. The enrolled passkey is bound to the console's **public** hostname, so an approval only verifies through
  that origin. The hostname must not change: the proxy keeps serving the console under the same public hostname and
  is repointed to 4786 in the window, **before** the approval check (9.5). `cerberus.passkeys.allowed_origins` is
  migrated from `web.public_url`, plus `http://localhost:4786` for keys enrolled later. Today the origins are derived
  from the old login-key file. The rehearsal compares each stored credential's `rpIdHash` with
  `sha256(host of an allowed origin)` and fails if any key matches none.
- **Restarts the owner will see:**
  - `os_service` units (Cerberus-written and native) are never restarted.
  - Plugin processes and running dev sessions live in the daemon's cgroup. They restart at phase B, at the handoff
    and at the re-home in F, each taking 30 s or less.

## 2. What runs today (by role)

| Thing | Today (code reference in `inventory-runtime.md`) |
|---|---|
| Daemon | User unit `com.hollis-labs.cerberus.service` written by `cerberus install`, running `cerberus daemon --foreground`, `Restart=always`, default `KillMode=control-group`. Unix socket `~/.cerberus/cerberus.sock`, `daemon.lock`, pid file. Its cgroup holds the plugin processes and the dev-session trees. |
| Console | A separate `cerberus web` process on loopback, from a copy of the same binary under another name. |
| Workloads | `os_service` resources (own units, `Restart=always`, independent of the daemon), native units tracked as resources, `dev_session` resources (children of the daemon), unsupervised handles (ssh, docker). Definitions come from `registry.yaml` plus `config.yaml`. |
| Self-deploy | By hand: `mv` the new binary over the installed path, then restart the unit from outside. Kept `.prev-<date>-<sha>` copies are the rollback. The self-guard refuses a deploy over the daemon's own socket. |
| Outside jobs that read Cerberus state | An outside backup job reads Cerberus state, and its path list must follow the new layout. A token renewal job writes into `events-tokens/`. Details are in the live-facts item. |

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
internal/api/            HTTP handlers; every route has a scope; the legacy-route set (section 6)
internal/mcp/            typed tools; names unchanged
internal/webui/          embeds all:dist (dist/.gitkeep kept)
pkg/                     connector, plugin, resource, secret, outpost: public contracts, unchanged
web/, ui/app.json        the console on app-shell
```

**Two roots.**
- **The data dir** `~/.local/share/cerberus-substrate/` (0700) holds what new owns and writes:
  - SQLite (host monitoring, pause state, applied digests, credential-issuance receipts);
  - the hash-chained logs (audit, approvals, host history), kept as JSONL in their current format;
  - the passkey registry files;
  - state snapshots;
  - the signing key.
- **The adopted root** `~/.cerberus` (config key `cerberus.adopt_root`) holds what new reads and writes **in place**,
  because live units, other tools or recorded absolute paths point into it:
  - `apps/`, the install root of the live units;
  - `plugins/`, `plugin-data/`, `plugin-cache/`, `plugin-connectors.json` (bundle digest paths are recorded);
  - `outpost-tokens/` (named by absolute path in `hosts.yaml`);
  - `events-tokens/` (written by the renewal job);
  - `secrets/` and `credential-issuance/secrets/issued/`;
  - `transfers/`.

  New never reads `~/.cerberus/paused*`, `standby`, `standby.d/`, `pids/`, `locks/`, `daemon.lock` or the socket (a test asserts
  this).

Config: the operator override is `~/.config/cerberus/substrate.yaml` (`SUBSTRATE_CONFIG`).

## 4. Module mapping

### 4.1 Substrate replaces (deleted from the app)

| Old | New |
|---|---|
| global config load, env reads, `CERBERUS_DB_PATH`/`CERBERUS_WORKSPACE` | `config` module + `internal/settings`; env table in `docs/conversion-notes.md` |
| ad hoc logging, unrotated `cerberus.log` | `logging` |
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
  disk. The fixtures are taken from the host at rehearsal, kept out of the repository, and the test reads them from
  a path given by an environment variable. Otherwise the first apply after cutover restarts workloads.
- **Self identity.** New knows which resource it is from config (`cerberus.self_resource_id`), checked at start
  against its own systemd unit (`/proc/self/cgroup`). It refuses deploy, apply, sync, reload, stop or remove on
  that resource through its own API. A test covers the Linux case.
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
- prints a report: per store, source sha256 (logical state for `main.db`), record counts before and after,
  digests, chain verification, and `source_unchanged: true`.

The snapshot is taken with old in standby (D3). `main.db` is copied with `sqlite3 main.db ".backup <copy>"` (it is in
WAL mode), never `cp`.

Treatments, using the vocabulary of `inventory-data.md`. Every row of that inventory falls into one of these lines.

| Store | Treatment | Verified by |
|---|---|---|
| `main.db`: `host_samples`, `host_status`, `host_sample_rollups`, `host_rollup_state`, `host_events` (+ the `sqlite_sequence` value) | **transform** into the new schema, every row, ids kept | `integrity_check`; per-table counts; `EXCEPT` both ways 0 on the copy |
| `main.db`: `projects`, `resources`, every `cerberus_schedule_*` and `gosched_*` table | **assert 0 rows**, then regenerate. If any has rows on the day, the window stops: it becomes a transform first. | counts printed |
| `main.db`: `schema_migrations` | regenerate (goose); the import refuses any old version set other than {1,2,3,5,6,7,8} | report |
| `audit/*.jsonl` + `audit/.digest_key` (secret, 0600) | **copy** byte for byte into the data dir; new continues the chain from its head in the same format | `audit verify` on the copy before and after; sha256 of the key |
| keyring `cerberus-audit-anchor` (account = audit dir path) | **transform**: write the same head under the new dir's path; the old entry stays | `audit verify` against both anchors |
| `approvals/events.jsonl` | **copy** byte for byte; new continues the chain | chain verify; line count |
| `approvals/passkeys/keys.json`, `key-counters.json` | **copy** byte for byte (no re-serialisation; the registry hash recorded in audit must match, or a 24 h cool-down starts) | sha256 equal; one approval on the rehearsal copy verifies with no cool-down |
| `hosts.history.jsonl` (+ rotations), `state/hosts.json`, `state/overview_snapshots.json` | **copy** | sha256 |
| `runtime/applied.json` | **transform** into SQLite, digest for digest | per-resource digest equal; observe mode reports no resource as changed |
| `paused`, `paused.<id>` | **transform** into pause state (operator intent) | set equal |
| `credential-issuance/<id>/*.json` | **transform** (receipts) | counts |
| `policy/` (`applied.yaml`, `applied.sha256`, `posture.yaml`, `enforcement.yaml`, `providers/*`) | **copy** into the config dir; `applied.sha256` is checked against `applied.yaml` before and after | sha256; `policy show` equal on both |
| `config.yaml`, `events.yaml`, `connector-config.yaml` | **transform** to config keys (table in conversion-notes; unknown key = error). Event export stays off. | `cerberus config` reviewed against the old values |
| `registry.yaml` | **copy**; the descriptors it lists stay in place (they belong to their apps) | entry and resource counts equal (observe mode) |
| `hosts.yaml` | **copy**; `token_file` paths keep pointing into the adopted root | `hosts list` equal |
| `connector-secrets.yaml` (secret refs) | **copy** at 0600 | sha256 |
| keyring `cerberus` service: connector and plugin credentials, `cerberus-oauth/issuer-ed25519` | **in place**: same service and account names (a contract) | one credentialed health op succeeds |
| the adopted root (section 3) | **in place**. Exactly one daemon runs plugins at any time. | plugins load with the same digests |
| `pids/<id>.{pid,meta}` | not copied. The dev sessions old was running are listed by standby (7.1) and started under new at D7. | session status after D7 |
| `standby.d/` (old's standby chain and session list, 7.1) | **not copied at D**. It is old's own record of the window. At F it is archived beside the old tree, and new imports it read-only as a sidecar log. | chain verify at F |
| `brakes/`, `oauth/tokens.jsonl`, `events/`, `alerts/` | absent live; the window re-checks. **copy** if present. | listed in the report |
| `web/login-*.key` | not migrated: replaced by identity sessions | — |
| `cerberus.log`, `logs/`, `backups/`, `*.bak*`, operator runbook directories under `<XDG_DATA_HOME>/cerberus/` (names in the live-facts item), the 0-byte `cerberus.db` | **archive**: left where they are and never deleted; the owner decides | listed in the report |
| `<XDG_STATE_HOME>/cerberus/` (backup passphrases, database passwords) | **in place**; not Cerberus state, but must never be lost | listed |
| socket, `cerberus.pid`, `daemon.lock`, `locks/`, `*.lock` | runtime; not migrated | — |

**The D3 manifest** records:
- for each old file that `export-legacy` could touch, its sha256;
- for `main.db`, **logical state, not bytes**: per table, the row count, `max(rowid)` and a digest of the ordered
  `.dump` of the five host tables. A WAL checkpoint or a restart of old changes the bytes without a logical write.

The standby chain and session list are excluded from the manifest.

**Rehearsal (cell, scratch directory, never the live tree).**
1. Take a `.backup` of `main.db` and a `cp -a` of `~/.cerberus` (old running normally; the rehearsal does not need
   standby).
2. Point `HOME`, `XDG_*` and `CERBERUS_DATA_DIR` at the scratch directory. Use a scratch keyring (`dbus-run-session`
   with its own gnome-keyring), because the anchor lives in the keyring.
3. Run `migrate-from` three times. Record counts, digests, both chain checks and timing.
4. Start new in `observe` mode on the copy with a fake supervisor backend. It must list the same resources and pause
   set as the copy, and no "changed" resource.
5. Verify one approval with a copied passkey and no cool-down.
6. Run `export-legacy` back onto a second copy, then run the old binary's `audit verify` there.
7. Run old with the slice 0 build on a third copy: standby entry, the allowed verbs, exit. The main chain, its anchor
   and the manifest-logical state of `main.db` must be unchanged.

**Reverse path (rollback).** `cerberus export-legacy --since <handoff>`:
- refuses unless every file in the D3 manifest still matches (sha256 for files, logical state for `main.db`). It
  fails closed and reports the first mismatch.
- appends the audit and approvals records written after the handoff to the old files (the same format and chain,
  continuing from the head the snapshot recorded) and moves the anchor back;
- copies host history, the pause set, applied digests and host-monitor rows newer than the snapshot back into
  `main.db` and the old files;
- leaves the new data dir intact.

## 6. Clients

From `inventory-clients.md` (by role). What changes for each:

| Caller | Interface | Change |
|---|---|---|
| Agents and skills (`deploy`, `service-start`, `service-stop`, ad hoc) | `cerberus` CLI on `PATH` | The verbs, flags and output they use stay the same; the CLI reads its key file. At D8 the CLI on `PATH` becomes new. |
| Long-lived `cerberus mcp` stdio children and the Tether gateway's cerberus entries | stdio MCP, `cerberus_*` tool names | Tool names and shapes stay the same. Children started before D8 dial old's socket; in standby, old refuses writes with an error naming the new CLI, and marks reads `standby: true`. **D9 restarts the gateway's entries**; harnesses respawn the rest. The MCP server id stays `cerberus`: renaming it disables the gateway's authority gate for it. |
| **tachyon** (machine sync, cerbcontrol) | old socket API through its Cerberus address setting: `/health`, `/resources`, `/hosts`, verb POSTs | **Named change, owned by tachyon's cell**: set its Cerberus address to new's HTTP URL and its token file to a key from D6. New serves the **legacy-route set** (these paths and shapes, under scope) on its HTTP API, so the change is configuration only. It must be ready before D and is applied at D9. |
| **tether sysop** | old socket at a hard-coded path, `POST /resources/{id}/{action}` | **Named change, owned by Tether's cell**: a code change to take a configurable address and token, pointed at new's HTTP API (legacy-route set). It must be merged and deployable before D and is applied at D9. |
| Browser | console | New port 4786; sign-in through identity; passkeys carried; proxy (if any) changed in D. |
| Outposts | outpost protocol, tokens in the adopted root | Unchanged; the open mount verifies its own tokens. |
| Hadron (`cerberus_op`) and other API or MCP machine callers in the inventory | MCP / API | Each gets an API key with its scopes in D6. |
| `cerberus-plugins` and third-party plugins | `pkg/plugin`, `plugin.yaml`, capabilities | Unchanged (contract). |
| Go importers of `github.com/hollis-labs/cerberus/pkg/...` | module path | Unchanged (contract). |
| Every repo's `*.cerberus.yaml` | `cerberus-project/v1` | Unchanged (contract). |
| `fleet-install` | drop-ins in Cerberus-written units | Unchanged; renderer parity keeps them valid. |
| The outside backup job | reads Cerberus state (list in the live-facts item) | Its path list gains the new data and config dirs (D, by its owner). The old paths stay until F. |
| The events token renewal job | writes `events-tokens/` | Unchanged (adopted root). Export stays off; turning it on later is a separate change with Tether's owner. |
| Processes old spawns that call `cerberus` through `PATH` | CLI | Old's unit `PATH` is pinned in phase B to put the legacy binary first. |

**What tachyon and tether sysop see during the shadow and soak** if their change is not applied: before D2 nothing
changes. From D2 until their change, writes are refused with the standby error and reads return old's frozen state,
marked `standby: true`. **The window does not start until both changes are ready**, so this gap is minutes, between
D2 and D9.

**Contracts with other apps.** The legacy-route set is a contract new keeps. Its list is derived from the two
callers' code in slice 4. No other contract changes. Changing any would need the director.

## 7. Cutover: self-deploy sequencing

Old means the running build, new the port. Phases are separate decisions. Phases B, D and F change the live host
and are run by the director with the owner, never by this cell. The exact commands and expected outputs are in
`cutover-runbook.md` (slice 12); host-specific paths and counts come from the live-facts item.

**Ordering.** Tachyon cuts over first, through old. Cerberus's phase D starts only when all of these hold:
- Tachyon is accepted;
- tachyon's and tether sysop's client changes are ready (section 6);
- no other app cutover or Cerberus-driven deploy runs from D until new is accepted at the end of E.

**Paths.**
- `$LEGACY`: `~/.local/libexec/cerberus-legacy/<sha256-prefix>/cerberus`, the pinned old binary (phase B).
- `$NEW`: the `fleet-install` link for new.

**Every "through old" command uses `$LEGACY` explicitly**, never `cerberus` on `PATH`, because from D8 onwards that
is new.

### 7.1 The standby mode (a live-app change on `main`, slice 0)

When the flag file `~/.cerberus/standby` exists, at start or when it appears (polled each second), old does the
following.

On entry:
- **stops plugin processes** and does not start them;
- **stops the loops**: host monitor and janitor, approval-expiry sweeper, snapshot recorder, resource monitor, event
  export;
- **stops every running `dev_session`** without writing `paused.<id>`. It records their ids in
  `~/.cerberus/standby.d/sessions.json`, outside the pause set. The file is written once, at the first entry; a later
  entry while it exists leaves it unchanged, so a restart of old in standby cannot shrink the list.

While in standby:
- **The socket** refuses every request except reads and `resource status|start|stop|reload` on the one resource named
  in the flag file. The refusal is guidance naming the new CLI. Reads carry `standby: true`.
- **The allowed verbs on the flagged resource** do not write `paused.<id>` or `runtime/applied.json`. They need no
  approval: policy is asserted to gate nothing on that resource, with a test.
- **Audit records for those verbs** go to a **separate chain**, `~/.cerberus/standby.d/audit-<date>.jsonl`, with its
  own genesis and its own digest key, not the main chain. Nothing else under `~/.cerberus` or in `main.db` is
  written: the main audit chain, its anchor and `approvals/events.jsonl` stay byte-identical.

Removing the flag returns old to normal: loops and plugins start again, and the sessions in `sessions.json` are
started again.

Tests in slice 0:
- each loop stops and restarts;
- plugins stop;
- dev sessions stop and are listed, with no pause flag written;
- the allowed set is exact (table-driven over every socket route);
- the main chain file, its anchor (fake keyring) and `main.db`'s logical state are unchanged across entry, a
  stop/start/reload of the flagged resource and exit.

### 7.2 Phases

**A. Prepare (cell, no host change).**
- CI builds new on `substrate-port`.
- The section 5 rehearsal passes, including step 7.
- The renderer parity fixtures pass.
- Slice 0 is merged to `main` through @substrate-manager.
- The runbook exists.
- The section 6 client changes are ready (director, with their cells).

**B. Pin old and give it standby (director).** Old restarts once. Plugins and dev sessions restart; no unit does.
1. Copy the running CLI and console binaries byte for byte as rollback copies, with sha256.
2. Build the slice 0 release from `main` and place it at `$LEGACY` (and the console binary beside it).
3. Add a drop-in to old's daemon and console units with:
   - `ExecStart=` on `$LEGACY`;
   - `Environment=PATH=` with `$LEGACY`'s directory first.
4. `systemctl --user daemon-reload` and restart both.
5. Check: `$LEGACY daemon status` (origin systemd, no standby), `$LEGACY resource list` equal to before, plugins
   loaded, dev sessions back within 30 s.

**Rollback:** remove the drop-ins and restart; old runs the untouched installed binary again.

**C. Shadow (director; no pause).**
1. Register a descriptor in old's registry for the resource `cerberus-substrate`, project `cerberus`:
   - `os_service`, systemd user;
   - **no build strategy and not `run_from: artifact`**: `command: [$NEW, serve]`;
   - env `SUBSTRATE_CONFIG`, `CERBERUS_DATA_DIR`;
   - health `/healthz` on 4786.

   `fleet-install` alone places and links the binary (dry run first). Old never builds or deploys this resource; it
   only applies, starts, stops and reports on it.
2. New starts with an empty data dir in `mode: observe`, `cerberus.self_resource_id: cerberus-substrate`.

Soak for at least 24 h. Checks:
- new's resource list and statuses equal old's (diffed);
- renderer parity against the live unit files;
- nothing under `~/.cerberus` or in `main.db` written by new (mtime and inode check);
- no plugin process with new as its parent.

**D. Handoff window (director with owner; a write pause of minutes).**
1. Announce a freeze on Cerberus mutations. Re-check:
   - the schedule, project and resource tables are still empty;
   - which of `brakes/`, `oauth/` and `events/` exist.

   Any surprise stops the window.
2. Stop the old console unit. `echo cerberus-substrate > ~/.cerberus/standby`. Wait until `$LEGACY daemon status`
   reports standby, plugins stopped and the sessions listed.
3. **Snapshot.**
   - `sqlite3 main.db ".backup <dir>/main.db"`, then `integrity_check`.
   - `cp -a ~/.cerberus <dir>/cerberus-home`, excluding the adopted root's directories and `standby.d/`.
   - Write the D3 manifest (section 5).
   - All of it goes into a private 0700 directory.
4. `$LEGACY resource stop cerberus-substrate` (recorded in the standby chain only). Move new's observe-mode data dir
   aside.
5. `$NEW migrate-from --old-home <dir>/cerberus-home --old-db <dir>/main.db`. Read the report.
6. Offline identity:
   - The owner runs `$NEW identity setup --username <owner>` at a terminal.
   - Then create keys with `--out` (0600): `cerberus-cli` (read, operate, admin), one for tachyon, one for tether
     sysop, `hadron`, and one per other machine caller in section 6.
7. Hand over.
   - Set `mode: supervise` in the override file, then `$LEGACY resource start cerberus-substrate` and
     `$LEGACY resource status cerberus-substrate`.
   - New loads the plugins and starts the host monitor.
   - `$NEW resource start <id>` for exactly the ids in `~/.cerberus/standby.d/sessions.json`.
8. Point the CLI on `PATH` at `$NEW` (atomic rename). Old is unaffected: it runs from `$LEGACY`.
9. Apply the tachyon and tether sysop changes, the proxy entry (if any) and the backup job's path list. Restart the
   Tether gateway's cerberus entries. Run the section 9 checks. Then lift the freeze.

Old stays running in standby. It owns the `cerberus-substrate` unit and can stop, start or roll back new. Its only
writes go to the standby chain.

**E. Soak (at least 7 days).** New supervises. Daily checks:
- both audit chains verify;
- the old main chain and `main.db`'s logical state still match the D3 manifest;
- pause state is as expected;
- no unexpected restarts (`ActiveEnterTimestamp` of every workload unit unchanged, excluding `cerberus-substrate`
  itself and any unit the owner restarted);
- host monitoring rows grow on new only.

**F. Retire old and re-home new (director).**
1. Stop and disable old's daemon and console units (unit files and `$LEGACY` kept).
2. Archive `~/.cerberus/standby.d/` beside the old tree; new imports its chain read-only as a sidecar log.
3. Re-home new on the canonical unit `com.hollis-labs.cerberus.service`: a `fleet-install` drop-in with `ExecStart` on
   `$NEW`, `cerberus.self_resource_id` set to the daemon. Remove the transitional `cerberus-substrate` unit and its
   descriptor.

   This restarts new only; its plugins and dev sessions come back within 30 s. From here on, **Cerberus is upgraded
   by `fleet-install`, never by itself**, and the self-guard stays.
4. Merge `substrate-port` into `main` (director, through @substrate-manager).

`~/.cerberus` stays as the adopted root. Moving its directories out is follow-up work, one unit at a time.

## 8. Rollback

| From | Rollback |
|---|---|
| B | Remove old's drop-ins and restart: back on the untouched installed binary. |
| C | `$LEGACY resource stop cerberus-substrate`, remove the descriptor; move new's data dir aside. |
| D, before step 7 | `rm ~/.cerberus/standby` (old restarts its loops, plugins and the listed sessions); start the old console. Old's state is as at D2. |
| D step 7 to the end of E | 1. `$LEGACY resource stop cerberus-substrate` (new stops its plugins and dev sessions; the standby chain records the stop). 2. `$NEW export-legacy --since <handoff>`, which fails closed on any D3 manifest mismatch. 3. Repoint the CLI on `PATH` to the legacy binary. 4. Revert the section 6 client changes (tachyon, tether sysop, proxy). 5. `rm ~/.cerberus/standby`: old restarts its loops and plugins and starts exactly the sessions in `sessions.json`. 6. Start the old console. 7. `$LEGACY audit verify`. No `os_service` workload restarts. |
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
3. New's resource list and statuses equal the D3 snapshot; pause set equal; no resource reported as changed (no unit
   rewritten, no restart).
4. One write the owner chooses (pause and resume a non-critical resource). It is recorded in new's audit, and `audit
   verify` passes across the old head into new's records against the new anchor. The old main chain is unchanged.
5. One approval through the console **at its public hostname** (the proxy already repointed in D9) with the carried
   passkey: no cool-down.
6. One MCP tool call through the restarted Tether gateway entry; the approve tool is refused under local trust.
7. tachyon's machine sync and tether sysop's resource action both succeed against new with their keys.
8. One outpost reports in; one credentialed connector health op succeeds (keyring in place).
9. Plugins are all parented by new and none by old; the sessions from `sessions.json` run under new.
10. SIGTERM to new (through old): new stops within its grace; no workload unit restarted.

## 10. Branch policy

- Port PRs target `substrate-port`. Docs-only PRs about the port and slice 0 (a live fix) target `main`.
- Merges go through @substrate-manager after reviewer approval at the head SHA and green CI. The lead sends the PR
  URL and the reviewed SHA.
- **Every docs PR passes the public-repo check:** no host names, no live-machine map.
- **Keeping in step with `main`.** The lead opens a `sync/main-<date>` PR merging `main` into `substrate-port`:
  - weekly, and within a day of any `main` merge that changes live behaviour;
  - reviewed like any PR;
  - where a fix touches code the port replaced, the merge takes the port's side and the lead files a task to carry
    the fix into the new code. The PR lists each one.

## 11. Slices (Torque tasks; each one PR)

| # | Base | Slice |
|---|---|---|
| 0 | `main` | Standby mode in old (section 7.1), with tests. |
| — | `main` | scan-public tree triage (allowlist and real fixes), so the port's CI gate is green. |
| 1 | `substrate-port` | Skeleton: manifest, wiring, entry point, settings, data dir, startup guard, CI. |
| 2 | `substrate-port` | Store schema (goose) and registry (v1 descriptors, `registry.yaml` index). |
| 3 | `substrate-port` | Supervision in observe mode: local connector, renderer with byte-parity fixtures (from an env path), status, self identity. |
| 4 | `substrate-port` | Identity, scopes, read API, the legacy-route set, CLI over HTTP with a key file; scope-coverage test. |
| 5 | `substrate-port` | Supervise mode: apply, deploy, start, stop, reload, artifacts, pause state, dev sessions, self-guard. |
| 6 | `substrate-port` | Audit (same format, anchor), approvals and presence (byte-exact registry), policy, redaction, egress, brakes. |
| 7 | `substrate-port` | Admin lane: ssh, docker, external connector service, plugin host (`pkg/plugin` unchanged, adopted roots), secrets (keyring names unchanged), credential broker. |
| 8 | `substrate-port` | Hosts and probes, janitor, scheduling core (off), outpost mount, event export (off), snapshot recorder. |
| 9 | `substrate-port` | MCP: HTTP and stdio, tool-name parity test, local scopes without approve. |
| 10 | `substrate-port` | `migrate-from`, `export-legacy`, verify, the standby-sidecar import, and the rehearsal (section 5, all seven steps). |
| 11a to c | `substrate-port` | Console on app-shell: (a) contract and scaffold, (b) pages, (c) sign-in and the approval ceremony. |
| 12 | `substrate-port` | Deploy: `cerberus-substrate` descriptor, `fleet-install` descriptor, `cutover-runbook.md`. |

Dependencies:
- the triage and slice 1 before every `substrate-port` merge;
- 2 before 3;
- 3 and 4 before 5;
- 4 before 9 and 11b;
- 5 to 9 before 10;
- 0 and 10 before 12.

## 12. Open points

- Can Substrate `pluginhost`, `scheduler` and `secrets` carry Cerberus's contracts unchanged? Answered in slices 7
  and 8; gaps go to project Substrate through @substrate-manager.
- The client changes for tachyon and tether sysop (section 6) need owners and a schedule from the director.
- The history findings of `scan-public` (old commits) go to the program-wide history pass, through @substrate-manager.
- The owner of the backup job and of the console proxy (if any) is named in the runbook before D.
- `transfers/` (operator payloads) is adopted in place. Whether to move it later is the owner's call.

## 13. Inventories

| Task | Output |
|---|---|
| CW-20261114-0088 | `docs/substrate-port/inventory-runtime.md` (#74) |
| CW-20261114-0086 | `docs/substrate-port/inventory-data.md` (#77) |
| CW-20261114-0090 | `docs/substrate-port/inventory-clients.md` (#75) |
| CW-20261114-0091 | `docs/substrate-port/inventory-gui.md` (#76) |

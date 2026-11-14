# Data inventory: every place Cerberus keeps state

Prep for the Substrate port (mission CW-20261112-0099, task CW-20261114-0086). The owner rule applies: code is a clean
break, data is not, and nothing listed here may be lost. This is the input to the port plan's data-migration section.

Taken on 2026-11-14 from the operator host, read-only: `ls`, `stat`, `du`, `wc -l`,
`sqlite3 "file:…?mode=ro"`. No file was written, locked, copied or opened read-write. No secret value was read or printed:
secret files are named and sized, never opened. Code references are to `main` at `493a9f0`. The live binary `ce767e6`
differs from it by two commits that touch no store. Paths use `~` for the operator's home. This repository is public:
live values (row counts, sizes, file counts, dates, host names) are in Tesseract workspace item `01M7DX799TQJ1FH6JXAD48GYP9`
(`project/substrate-migration/workspace/cerberus`, key `inventory-data-live`); this document keeps the schema, the
writers and the treatments.

Writers were found with:

```sh
grep -rn "os.WriteFile\|os.Create\|OpenFile\|sql.Open\|MkdirAll" internal cmd pkg --include=*.go | grep -v _test.go
# 138 hits in 27 packages
```

plus a second pass for writers that go through helpers: `os.Rename`, `CreateTemp`, `flock`, `net.Listen("unix")`,
go-keyring, and the libraries that write on Cerberus's behalf (`libs/core/util` apppaths and scheduler `sqlstore`,
`libs/core/store`, `libs/mesh/eventbus`, `libs/security/secrets`, `zalando/go-keyring`). Every package from the grep appears
in section 6.

## Summary

- **Live SQLite database:** `~/.local/share/cerberus/workspaces/default/main.db`, not `cerberus.db`. The file
  `~/.local/share/cerberus/cerberus.db` is 0 bytes, and no code in this repository or in apppaths creates that name.
- **Stores:** 64 entries in total.
  - 1 SQLite database with 24 tables plus `sqlite_sequence` (section 1).
  - 54 entries in sections 2 to 5: logs, configuration and state, 3 OS keyring entries, and stores outside `~/.cerberus`. 9 of them are written by operators or runbooks outside this repository.
  - 9 runtime-ephemera entries that are regenerated rather than migrated.
- **Rows that hold data:** only the host-monitoring tables did at inventory time. The project, resource and scheduling tables were empty.
- **Data that is hard to migrate:** two hash-chained logs, the audit log and the approvals log. The audit chain's anchor
  lives in the OS keyring, keyed by the audit directory's absolute path. Moving that directory breaks verification unless the anchor is moved with it (section 2).
- **Secret-bearing stores:** 12, plus 1 that may hold secrets (`cerberus_schedule_logs.body`, which is empty). All are marked 🔒 below, and none was opened.

Treatment vocabulary:

- **copy**: byte-for-byte to the new location, then verified with a checksum.
- **transform**: read and rewritten into the new schema or format, then verified with counts.
- **in place**: the new app reads the existing path; nothing moves.
- **regenerate**: the new side recreates it, and the old copy is moved aside, never deleted.
- **archive**: not application state; moved aside with the old tree, and the owner decides when to delete it.
- **drop**: empty, or dead code; recorded as such and moved aside.

## 1. SQLite: `~/.local/share/cerberus/workspaces/default/main.db`

`journal_mode=wal`. Opened through `libs/core/store`
(`internal/store/sqlite/store.go:28`, migrations at `:35`). The path comes from apppaths (`internal/config/layout.go:9-19`)
and can be overridden with `--db`, `CERBERUS_DB_PATH` or `CERBERUS_WORKSPACE`. Holds secrets: no, apart from the
note on `cerberus_schedule_logs.body`. Version 4 of the migrations is the bootstrap and is skipped by design (`internal/store/sqlite/migrations.go:52-55`).

| Table | Columns | DDL | Treatment |
|---|---|---|---|
| `host_samples` | host TEXT, ts INTEGER, load1 REAL, load5 REAL, load15 REAL, cpu INTEGER, mem_total INTEGER, mem_available INTEGER, disk_total INTEGER, disk_free INTEGER, uptime_s INTEGER, probe_ms INTEGER | `migrations/006_create_host_monitoring.sql:4` | **transform**: a rolling 7-day window of probes per host that the janitor prunes. Copy every row, so no history is lost at cutover. |
| `host_status` | host TEXT, status TEXT, reasons TEXT, since INTEGER, last_ok INTEGER, last_attempt INTEGER, next_due INTEGER, consecutive_failures INTEGER, candidate TEXT, candidate_count INTEGER | `006:20` | **transform**: current status per host; `since` and `consecutive_failures` cannot be rebuilt. |
| `host_sample_rollups` | host TEXT, grain TEXT, bucket_ts INTEGER, n INTEGER, load1_n INTEGER, load1_sum/min/max REAL, mem_avail_sum/min/max INTEGER, disk_free_sum/min/max INTEGER, probe_ms_sum INTEGER, probe_ms_max INTEGER, uptime_max INTEGER, cpu_max INTEGER, mem_total_max INTEGER, disk_total_max INTEGER | `007:6` | **transform**: the only long-term history (5m/1h/1d grains); the raw samples behind them are already pruned. |
| `host_rollup_state` | host TEXT, grain TEXT, watermark INTEGER | `007:30` | **transform**: rollup watermarks. Without them the new side would re-roll or double-count. |
| `host_events` | id INTEGER, ts INTEGER, host TEXT, kind TEXT, from_status TEXT, to_status TEXT, reasons TEXT, expected INTEGER (index `host_events_host`) | `008:4,14` | **transform**: status transitions. Keep `id`, and carry the `sqlite_sequence` row for `host_events` so ids stay monotonic. |
| `projects` | id TEXT, name TEXT, description TEXT, created_at TEXT, updated_at TEXT | `001:1` | **drop**: empty at inventory time, because definitions live in YAML descriptors. Re-check that it has 0 rows at cutover. |
| `resources` | id TEXT, name TEXT, type TEXT, project_id TEXT, connector TEXT, config TEXT, tags TEXT, depends_on TEXT, created_at TEXT, updated_at TEXT | `002:1` | **drop**: empty, same reason. |
| `schema_migrations` | version INTEGER, applied_at TEXT | `migrations.go:19` | **regenerate**: the new side uses its own goose set. |
| `cerberus_schedule_receipts` | fire_id, job_key, revision, state, plan_hash, error (TEXT) | `internal/scheduling/core.go:105-115` | **regenerate**: the scheduling core has never been started (see the runtime inventory), so the table was empty. Assert 0 rows at cutover; if any table has rows, transform it instead. |
| `cerberus_schedule_generations` | job_key TEXT, generation INTEGER | `core.go:105-115` | same |
| `cerberus_schedule_creates` | owner_app, idempotency_key, fingerprint, job_key TEXT, incarnation INTEGER | `core.go:105-115` | same |
| `cerberus_schedule_registrations` | owner_app, request_key, fingerprint TEXT, bindings BLOB | `core.go:105-115` | same |
| `cerberus_schedule_desired` | job_key TEXT, incarnation INTEGER, fingerprint TEXT | `core.go:105-115` | same |
| `cerberus_schedule_manual` | job_key, request_id, fire_id TEXT | `core.go:105-115` | same |
| `cerberus_schedule_logs` | fire_id, job_key TEXT, incarnation INTEGER, revision, claimed_at, stream, body TEXT, truncated INTEGER, capture_error INTEGER | `internal/scheduling/log_store.go:23-27` | same. 🔒 maybe: `body` holds job output. |
| `cerberus_schedule_outcomes` | fire_id, job_key TEXT, incarnation INTEGER, scheduled_at, claimed_at, state TEXT, observed INTEGER | `log_store.go:23-27` | same |
| `cerberus_schedule_episodes` | job_key TEXT, incarnation INTEGER, episode_id TEXT, failing INTEGER, misfires INTEGER, cursor TEXT | `log_store.go:23-27` | same |
| `cerberus_schedule_observations` | event_id TEXT | `log_store.go:23-27` | same |
| `cerberus_schedule_outbox` | event_id, job_key TEXT, incarnation INTEGER, episode_id, kind, fire_id, state TEXT | `log_store.go:23-27` | same |
| `gosched_schedules` | id, cron_expr, last_run, next_run TEXT, enabled INTEGER, job_type TEXT, payload BLOB, retry_json TEXT | `libs/core/util@v0.6.0/scheduler/sqlstore/schema/001_init.sql:6` (run from `core.go:102`) | **regenerate**: library-owned, so its DDL is not duplicated. Assert 0 rows. |
| `gosched_fires` | id, schedule_id, scheduled_at, fired_at, claim_expires_at TEXT, attempt INTEGER, status, next_attempt_at, last_error, retry_json, job_type TEXT, payload BLOB | `001_init.sql:20` | same |
| `gosched_pruned` | schedule_id, through_at TEXT | `002_retention.sql:3` | same |
| `gosched_schedule_options` | schedule_id, options_json TEXT | `003_options.sql:3` | same |
| `gosched_fire_options` | fire_id, options_json TEXT | `003_options.sql:7` | same |
| `sqlite_sequence` | name, seq | SQLite internal | carry the `host_events` value with the transform |

`pipeline_runs` (`003`) was dropped by `005_drop_pipeline_runs.sql` and is gone.

Row counts (`sqlite3 "file:main.db?mode=ro" "select count(*) from <table>"`) are in workspace item `01M7DX799TQJ1FH6JXAD48GYP9`. They move with
the live host monitor; take them again on the backup copy at rehearsal.

## 2. Hash-chained and append-only logs

| Store | Writer | Format | Secrets | Treatment |
|---|---|---|---|---|
| `~/.cerberus/audit/YYYY-MM.jsonl` | `internal/audit/sink.go:135,183,274`; dir `internal/app/app.go:267-272` | Hash-chained JSONL, fsynced; flock on `audit/.lock` (`sink.go:213-227`) | Credential names only, not values | **copy**, byte for byte. Verify with `cerberus audit verify` on the copy before and after. The new side continues the chain from the last head; it does not start a new chain. |
| 🔒 `~/.cerberus/audit/.digest_key` | `sink.go:56-58,91` | 32 raw bytes, HMAC key | **yes** | **copy** together with the audit files: without it the existing digests cannot be verified. |
| Keyring `cerberus-audit-anchor` / account = absolute audit dir | `cmd/cerberus/audit_anchor.go:16,43,52` | JSON `{seq,hash,file,offset}`, the chain head | No | **transform**: the account name is the directory path. If the audit directory moves, write the same head under the new path, or verification reports a missing anchor. |
| `~/.cerberus/approvals/events.jsonl` | `internal/approval/store.go:101,109,258-285` | Hash-chained JSONL, fsynced; in-process mutex only | No (argument digest, plan hash) | **copy** while the old daemon is stopped, so the chain cannot fork (see the runtime inventory, collision 17). |
| `~/.cerberus/approvals/passkeys/keys.json`, `key-counters.json` | `internal/presence/presence.go:59,801,811,827` | JSON, WebAuthn public credentials and sign counters | No (public keys), but they are an authority root | **copy**. Passkeys are bound to the console hostname, so they keep working only if the new console serves the same `web.public_url`. Counters must not go backwards. |
| `~/.cerberus/hosts.history.jsonl` (+`.1`, `.2`) | `internal/hosts/history.go:72,78-84` | JSONL, 1 MiB rotation | No | **copy** |
| `~/.cerberus/brakes/events.jsonl` | `internal/brake/brake.go:34,410-418` | Hash-chained JSONL | maybe (`proof`, `brake.go:251`) | Nothing to migrate today. Re-check at cutover, and **copy** if it exists. |
| `~/.cerberus/oauth/tokens.jsonl` | `internal/oauth/issuer.go:166,250-258` | JSONL, flock | Metadata only | Same: re-check, and **copy** if it exists. |
| `~/.cerberus/events/…` (export outbox, hub spool, cursors) | `internal/eventexport/outbox.go:65,95,275`; `exporter.go:86,187-201`; `cmd/cerberus/events_export.go:43,82-102`; `libs/mesh/eventbus@v0.2.0` | Hash-chained JSONL, cursors, spool | No | Same: re-check, and **copy** if it exists. |
| `~/.cerberus/alerts/*.json` | `internal/daemon/monitor.go:346-369` | JSON | No | **drop** |

## 3. Configuration and derived state under `~/.cerberus`

The **Who** column says who authors each file: **op** means the operator writes it by hand, and **cerb** means Cerberus writes it.

| Store | Writer | Who | Format | Secrets | Treatment |
|---|---|---|---|---|---|
| `config.yaml` | `internal/config/config.go:144-148` (seed only); restore `internal/configops/backups.go:63-91` | op | YAML v2 | maybe (literal env values) | **transform** into the new app's config block. Each key gets a mapping in `docs/conversion-notes.md` (`web.public_url`, `host_monitor.*`, and the empty `resources:`/`projects:`). |
| `connector-config.yaml` | read only (`internal/app`) | op | YAML | No | **copy** or **transform** (per-connector MCP exposure) |
| 🔒 `connector-secrets.yaml` | read only (`internal/pluginhost/secrets.go`, `internal/app/app.go:284,390`) | op | YAML of `keychain://` / `file://` refs | **yes**, references (literals possible) | **copy**, mode `0600`. The new app's secrets resolver must accept the same reference schemes. |
| `events.yaml` | read only (`internal/eventexport/settings.go:39-44`) | op | YAML | No | **transform** to config keys |
| `registry.yaml` | `internal/registry/index.go:84-101` | cerb (via `register`) | YAML, entries pointing at descriptor files | No | **copy**. The descriptor files it points at stay **in place**; they belong to each app. |
| an operator descriptor of ssh host handles (`<name>.cerberus.yaml`; name in the workspace item) | hand plus `cerberus host` edits | op | descriptor YAML, ssh resources | No | **in place** (referenced from `registry.yaml`) |
| `hosts.yaml` | `internal/hosts/edit.go:132,244,269` | op plus cerb | YAML with `token_file` references | No values; points at token files | **copy**. The `token_file` paths are absolute and must still resolve (see `outpost-tokens/` below). |
| `hosts.yaml.bak-*` (Cerberus keeps 20) and hand `.bak` / `.merged` copies | `edit.go:211-214` | cerb plus op | YAML | No | **archive** |
| `state/hosts.json` | `internal/hosts/state.go:122-144` | cerb | JSON | No | **copy** |
| `state/overview_snapshots.json` | `internal/cerbapi/snapshot_recorder.go:205-216,383-387` | cerb | JSON, 7-day window | No | **copy**: the dashboard's 24-hour trend; cheap to keep |
| `runtime/applied.json` | `internal/cerbapi/applied_definitions.go:86-93` | cerb | JSON, definition digests per resource | No | **copy**. The monitor compares against it (`resource_monitor.go:250-259`). Without it, every resource reads as changed and auto_restart is skipped. |
| `policy/applied.yaml`, `applied.sha256` | `internal/policy/store.go:233-240,295-300` | cerb | YAML plus hash | No | **copy** (`posture`/`enforcement` decide what runs) |
| `policy/posture.yaml`, `enforcement.yaml`, `providers/<id>.yaml` | `internal/policy/posture_file.go:100-103`, `enforcement_file.go:61-64`, `store.go:254` | op via the CLI | YAML | No | **copy**. Archive the `posture.yaml.bak-*`. |
| `paused`, `paused.<id>` | `internal/pausectl/pausectl.go:17-69` | op via the CLI | text marker | No | **copy**: these are operator intent, and a resource paused today must stay paused. |
| `plugin-connectors.json` | `internal/cerbapi/plugin_connector_state.go:60-68,112-126` | cerb | JSON v2 (installed plugins, grants) | No | **copy** |
| `plugins/<id>/<digest>/…` | `internal/pluginhost/bundle.go:156-287` | cerb | plugin bundles, content-addressed | No | **copy**. Digest paths are recorded in `plugin-connectors.json`. |
| `plugin-data/<hash>/` | dirs `internal/pluginhost/manager.go:943`; contents by plugins | plugins | plugin-defined | maybe | **copy** (empty today; re-check) |
| `plugin-cache/<hash>/` | same | plugins | cache | No | **regenerate** |
| `credential-issuance/<id>/{preparing,prepared,dispatching,completed}.json` | `internal/credentialbroker/broker.go:93,167` | cerb | JSON receipts | No (`file://` refs) | **copy** (issuance history) |
| 🔒 `credential-issuance/secrets/issued/<id>/<name>` | `broker.go:50,113` via `libs/security/secrets` | cerb | raw credential files | **yes** | **copy** in place, mode `0600`, never printed. Consumers hold these paths. |
| 🔒 `web/login-<listen-addr>.key` | `internal/webui/session.go:377-407` | cerb | JSON, console login key | **yes** | **regenerate**: per console process, removed on shutdown. |
| `apps/<project>/<id>/bin/`, `current/`, `install-manifest.json` | `internal/connector/local/artifact.go:114-131,336-388`; `install_layout.go:65-76` | cerb | binaries plus JSON | No | **in place**. Live units `ExecStart` from here and log here (see the runtime inventory). Moving them restarts workloads. |
| `apps/<project>/<id>/logs/{stdout,stderr,build}.log` | systemd; `build_log.go:25-64` | systemd plus cerb | text | maybe (raw service output) | **in place** |
| `transfers/` | `internal/cerbapi/transfer_root.go:113`; `internal/connector/ssh/transfer.go:112-128` | cerb (file-transfer root) | arbitrary files | maybe | **in place** or **copy**: operator payloads (outpost rollout bundles). Owner decides. |
| `backups/` | not written by this repository | op | mixed (old configs, binaries, a patch) | maybe (old `config.yaml`) | **archive** |
| hand-made backups of the config, connector secrets 🔒 and descriptor files, and other operator scratch files (names in the workspace item) | by hand | op | mixed | `connector-secrets` backups: **yes** | **archive** (mode kept) |
| `cerberus.log` | `internal/service/lifecycle_log.go:33-37` | cerb | JSON slog, redacted | No (redacted) | **archive**. The new side logs through Substrate's logging. Flag: this file grows without bound today. |
| `logs/systemd-{stdout,stderr}.log` | systemd (`cmd_install_systemd.go:87-90`) | systemd | text | No | **archive** |

### Secret-bearing stores Cerberus reads but this repository does not write

| Store | Who writes it | Treatment |
|---|---|---|
| 🔒 `~/.cerberus/outpost-tokens/<host>.token` | Operator or outpost enrolment, outside this repo. Named by absolute path in `hosts.yaml` `reach.outpost.token_file` (`internal/hosts/host.go:158`). | **in place**, or **copy** with a `hosts.yaml` path rewrite in the same step. |
| 🔒 `~/.cerberus/events-tokens/<name>/` (device token and its config) | a credential-renewal job outside this repo; a drop-in on the daemon unit names the token file (details in `01M7DX799TQJ1FH6JXAD48GYP9`) | **in place**: the renewal job writes this path. |
| 🔒 `~/.cerberus/secrets/<name>` | Hand-placed `file://` targets for connector credentials | **in place** (the reference in `connector-secrets.yaml` names it) |

## 4. OS keyring (gnome-keyring through Secret Service, `zalando/go-keyring`, `internal/secrets/keyring_ops.go:24-26`)

These entries were not enumerated, because listing them would mean querying the secret store. They are named here from the code.

| Service | Account | Content | Writer | Treatment |
|---|---|---|---|---|
| 🔒 `cerberus` | `<connector-or-plugin>/<key>` | connector and plugin credentials | `internal/secrets/keychain.go:21,61-69` (`cerberus secrets set`, the console credential form) | **in place**. The new app's secrets module reads the same service and account names, or a one-time copy runs under the same user. A clean-break rename here loses access to every credential. |
| 🔒 `cerberus` | `cerberus-oauth/issuer-ed25519` | Ed25519 seed for the built-in OAuth issuer | `internal/oauth/issuer.go:38-41,58` | **in place**, if the issuer is kept. Unused today: no `mcp-http.yaml`. |
| `cerberus-audit-anchor` | absolute audit dir | audit chain head | `cmd/cerberus/audit_anchor.go:16-55` | see section 2 |

## 5. Outside `~/.cerberus`

| Store | Writer | Secrets | Treatment |
|---|---|---|---|
| `~/.local/share/cerberus/cerberus.db` | unknown; no code here or in apppaths creates it | No | **drop** (move aside). It is not the database. |
| Runbook evidence and backup directories under `~/.local/share/cerberus/` (names and sizes in `01M7DX799TQJ1FH6JXAD48GYP9`) | runbooks outside this repo; no code here writes them | maybe (some hold encrypted backups) | **archive**, never deleted; moved aside with the old data dir. The owner decides when to delete. |
| 🔒 Runbook secret files under `~/.local/state/cerberus/` (backup passphrases, service passwords; names in `01M7DX799TQJ1FH6JXAD48GYP9`) | runbooks or plugins outside this repo | **yes** | **in place**. Losing the passphrases makes the archived backups unreadable. Flag for the owner. |
| `~/.local/state/cerberus`, `~/.cache/cerberus`, `~/.config/cerberus` (roots) | apppaths `materialize()` (`libs/core/util@v0.6.0/apppaths/layout.go:84-99`) | — | **regenerate** |
| `~/.config/cerberus/<host>.cerberus.yaml` and operator resource files beside it | by hand; listed in `registry.yaml` | No | **in place**: app-owned descriptors, not Cerberus state |
| `~/.config/systemd/user/com.hollis-labs.cerberus*.service` and the custom-named resource units | `internal/connector/local/systemd.go:367-390`; `cmd/cerberus/cmd_install_systemd.go:80-168` | maybe (literal `Environment=`) | **in place**. Hand-over is a cutover step, not a data copy. |
| `<repo>/.cerberus-build.lock`, `<repo>/<dist>/…`, `checksums.txt` | `internal/buildlock/buildlock.go:15-50`; `internal/connector/local/build_strategy.go:155,232,538` | No | not Cerberus state; **regenerate** |

### Runtime ephemera: regenerate, never migrate

`~/.cerberus/cerberus.sock` (`internal/cerbapi/socket_server.go:177-183`), `cerberus.pid` (`internal/daemon/pidguard.go:107-110`),
`daemon.lock` (`daemon_lock.go:46-164`), `pids/<id>.{pid,meta}` (`internal/service/pidfile.go:36-233`),
`locks/<id>.lock` (`lockfile.go:31-90`), `hosts.yaml.lock` (`internal/hosts/lock_unix.go:13-17`),
`plugin-connectors.json.lock` (`plugin_connector_state.go:159-164`), `audit/.lock`,
and `$TMPDIR/cerberus-<id>.log` (`dev_session.go:321-326`, `internal/service/service.go:233-241`; none live).

## 6. Writers by package

Every package with a hit in the grep, and where its writes land:

| Package | Paths |
|---|---|
| `cmd/cerberus` | daemon unit and plist (`cmd_install*.go`), audit anchor (keyring), events export wiring, web login key |
| `internal/app` | `main.db` open, store directories (`app.go:108-112,218-272`) |
| `internal/approval` | `approvals/events.jsonl` |
| `internal/audit` | `audit/*.jsonl`, `.digest_key`, `.lock` |
| `internal/brake` | `brakes/events.jsonl` (absent) |
| `internal/buildlock` | `<repo>/.cerberus-build.lock` |
| `internal/cerbapi` | `runtime/applied.json`, `plugin-connectors.json`, `state/overview_snapshots.json`, `transfers/`, socket |
| `internal/config` | `config.yaml` seed |
| `internal/configops` | `config.yaml` restore and `.pre-restore-*.bak` |
| `internal/connector/local` | units, plists, `apps/<p>/<id>/{bin,current,logs}`, `install-manifest.json`, dist artifacts, dev-session logs |
| `internal/connector/ssh` | downloads under the transfer root |
| `internal/credentialbroker` | `credential-issuance/` |
| `internal/daemon` | `cerberus.pid`, `daemon.lock`, `alerts/` (dead code) |
| `internal/eventexport` | `events/audit/` (off) |
| `internal/hosts` | `hosts.yaml` and backups, `hosts.yaml.lock`, `hosts.history.jsonl`, `state/hosts.json` |
| `internal/oauth` | `oauth/tokens.jsonl` (absent), keyring issuer key |
| `internal/outpostexecutor` | read-only opens only (`regular_file.go:57`) |
| `internal/pausectl` | `paused*` |
| `internal/pluginhost` | `plugins/`, `plugin-data/`, `plugin-cache/` |
| `internal/plugins/dockerplugin` | `<dir>/plugin.yaml` and `bin/` (operator-invoked prototype scaffold) |
| `internal/policy` | `policy/` |
| `internal/presence` | `approvals/passkeys/` |
| `internal/registry` | `registry.yaml` |
| `internal/service` | `cerberus.log`, `pids/`, `locks/`, `$TMPDIR` logs |
| `internal/webui` | `web/login-*.key` |
| `internal/smoke/confirmdialog`, `internal/testfixture/sentinel` | not production (a scratch-`HOME` smoke binary and a test fixture) |

Libraries that write on Cerberus's behalf: `libs/core/store` (main.db), `libs/core/util` apppaths (XDG roots) and
`scheduler/sqlstore` (`gosched_*`), `libs/mesh/eventbus` (hub spool, off), `libs/security/secrets` (issued credentials),
`zalando/go-keyring` (keyring).

## Notes for the port plan

These are inputs, not decisions.

- The main database is small and has a simple shape: 5 host tables with data, and the rest empty. A row-count and
  checksum rehearsal on a `.backup` copy is cheap.
- The two chains (audit, approvals) and the keyring anchor are the parts where a careless copy loses integrity rather than
  data. Copy them while the old daemon is stopped, verify on the new side, and move the anchor if the path changes.
- Several secret stores are referenced by absolute path from outside Cerberus: `hosts.yaml`, the daemon unit drop-in, the
  renew timer and `connector-secrets.yaml`. Leaving `~/.cerberus` in place and having the new app read it is
  the low-risk option; moving it means rewriting each of those references in the same step.
- `apps/` holds the binaries the live units run from. It is part of the unit hand-over, not the data copy.

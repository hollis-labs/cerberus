# Conversion notes: Cerberus to Substrate

Old-to-new mapping for operators, the console and callers. Started in slice 1 (CW-20261114-0108); each slice
adds its rows. The plan is `docs/substrate-port-plan.md`.

## Gaps (need a decision)

| Gap | Status |
|---|---|
| BOM does not pin `libs/mesh/eventbus` (event export) and `libs/security/webauthn` (passkey approvals); `substrate pins check` fails on those two lines. | Requested from @substrate-manager through the cell lead (add both to `app_libs`). |
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

## Commands

| Old | New |
|---|---|
| `cerberus daemon --foreground` | `cerberus serve` (also the default with no command) |
| Every other verb | Not on this branch yet; each returns in its slice with the same name and flags (plan section 6). |

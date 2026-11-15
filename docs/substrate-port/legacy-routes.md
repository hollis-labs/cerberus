# Legacy-route set (cross-app contract)

Task: CW-20261114-0169 (slice 4 of the port plan, `docs/substrate-port-plan.md` sections 6 and 11). Status:
**approved cross-app contract** (program director, 2026-11-14, at `935420b`; sent to the client owners, tachyon
CW-20261114-0144 and tether sysop CW-20261114-0145). Any change, the `/api` mount included, goes through the cell
lead to the program director.

This is the subset of the old daemon's socket API that other apps call today, and how the new build serves it.
Paths, query parameters, request bodies and response bodies keep the old shapes. What changes is where the routes
live (a loopback HTTP listener under `/api` instead of the unix socket) and how callers authenticate (an API key
instead of the socket's same-uid check).

Sources, read-only:
- tachyon at `8f8b018`, file paths relative to its repo;
- tether at `57029316`, sysop at `apps/sysop/cmd/tether_sysop/main.go`;
- the old server on cerberus `main`, `internal/cerbapi/socket_server.go` and handlers.

## 1. Address and authentication

| | Old (socket) | New |
|---|---|---|
| Address | unix socket `~/.cerberus/cerberus.sock` | `http://127.0.0.1:4786/api`, the base URL; routes below are appended to it |
| Who may call | any process running as the daemon's uid (peer credentials, `socket_server.go:971-986`) | a caller presenting an API key: `Authorization: Bearer key_…`. Without one, `401 unauthorized` |
| Key | none | issued offline (`cerberus identity key create --name <client> --scope … --out <file>`, file mode 0600); scopes per client in section 4 |
| `X-Cerberus-Api` | missing or `v1` accepted, anything else `400` (`socket_server.go:203-206`); response always carries `X-Cerberus-Api: v1` | unchanged |
| `X-Cerberus-Principal`, `X-Cerberus-Bearer` | self-reported principal, OAuth bearer | ignored; the principal is the key's |

**tachyon** needs no code change, only configuration:
- `CERBERUS_ADDR=http://127.0.0.1:4786/api`. All three clients join the base and the path by string concatenation:
  `internal/cerbcontrol/cerbcontrol.go:97-118,135`, `internal/objectsync/source.go:60-95`, and
  `internal/accessstatus/accessstatus.go` (`target`). The token guard compares origins, not paths
  (`internal/upstreamauth/upstreamauth.go:99-100`), and allows a token over plain http to loopback (`:35-58`).
- `CERBERUS_TOKEN_FILE=<key file>`. It is sent as `Authorization: Bearer` (`upstreamauth.go:108`).

**tether sysop** needs a code change (CW-20261114-0145). The socket path is hard-coded and no token is sent
(`main.go:5376-5394`). It needs a configurable base URL and a key file sent as `Authorization: Bearer`.

## 2. Routes

Scopes are from plan section 4.3. "Old type" is the Go type the old handler serialized (`internal/cerbapi/dto.go`
on `main`). The new build serializes the same fields with the same JSON names and `omitempty` rules. Bodies are
compact JSON. Lists are bare arrays, never `null`.

| # | Method | Path (after the base) | Query / body | Scope | Response (old type) | Callers (file:line) |
|---|---|---|---|---|---|---|
| L1 | GET | `/health` | `resource_id` (optional) | `cerberus.read` | `DaemonHealth`: `{"services":null,"resources":[ResourceHealth…],"daemon_running":true,"services_protected":0,"services_failed":0}`. `resources` is omitted when empty; an unknown `resource_id` is 200 with `resources` omitted | tachyon `internal/cerbcontrol/cerbcontrol.go:217-226` |
| L2 | GET | `/resources` | `project_id`, `connector`, `tag` (each optional, exact match) | `cerberus.read` | bare array of `ResourceInfo` | tachyon `cerbcontrol.go:167-179` |
| L3 | GET | `/resources/{id}/status` | none | `cerberus.read` | `ResourceRuntimeStatus` | tachyon `cerbcontrol.go:182-192`; sysop `main.go:5380-5384` |
| L4 | GET | `/resources/{id}/inspect` | none | `cerberus.read` | `ResourceInspect` | tachyon `cerbcontrol.go:182-192` |
| L5 | GET | `/resources/{id}/doctor` | none | `cerberus.read` | `ResourceDoctor` | tachyon `cerbcontrol.go:182-192` |
| L6 | GET | `/resources/{id}/logs` | `lines` (integer above 0, default 50; tachyon caps at 2000), `stream` | `cerberus.read` | `LogLines` | tachyon `cerbcontrol.go:195-214` |
| L7 | POST | `/resources/{id}/{verb}` for `reload`, `stop`, `deploy`, `apply`, `sync` | JSON body or **empty body** (sysop sends none): `acknowledged`, `approval_id`, `plan`, `confirmed_plan_hash`, `install_after_build_override`; unknown fields ignored; 1 MiB limit | `cerberus.operate` | `OpResult`. A runtime failure is still 200 with `{"success":false,"service_id":…,"error":…}`, as before | tachyon `cerbcontrol.go:229-234`, access probe `internal/server/register_access.go:35-36`; sysop `main.go:5371-5422` (`reload`, `apply`, `deploy`) |
| L8 | POST | `/resources/{id}/remove` | as L7 | `cerberus.admin` | `OpResult` | tachyon `cerbcontrol.go:229-234` (verb list `:53-63`) |
| L9 | GET | `/hosts` | `role`, `provider`, `state`, `label=k=v` (repeatable) | `cerberus.read` | bare array of `HostView`, sorted by name | tachyon `internal/objectsync/machines.go:84-100`, access probe `register_access.go:34`, `internal/accessstatus/accessstatus.go:192-225` |

`{id}` is one path segment, URL-unescaped. tachyon restricts ids to `^[a-z0-9][a-z0-9._-]{0,63}$` (`cerbcontrol.go:77`);
sysop escapes with `url.PathEscape` (`main.go:5381`).

### Fields the clients actually read

- **L1 to L8, tachyon `cerbcontrol`:** none. The body is passed through opaquely to tachyon's console proxy
  (`cerbcontrol.go:153-155`; `internal/server/register_cerbcontrol.go:152-156`). The whole old shape must hold.
- **L3, L7, sysop:** `success` (bool) and `error` (string) only (`main.go:5359-5362`). A 2xx with `success:false`
  and a non-empty `error` is a failure (`:5408-5413`).
- **L9, tachyon `objectsync`** (`machines.go:32-82`):
  - `HostView`: `host`, `state` (must be `online`, `offline` or `unknown`; any other value is an error, `:96-99`),
    `onboarding_stage`, `last_seen`, `probed_at`, `checked_at`, `stale`, `probed`.
  - `host`: `name`, `hostname`, `tailnet_name`, `tailnet_ip`, `lan_ip`, `role`, `provider`, `owner`, `labels`,
    `tether_environment_id`, and `declared` (`os`, `arch`, `cpu`, `memory`, `disk`, `gpu`).
  - `probed`: `source`, `os`, `os_version`, `arch`, `kernel`, `cpu`, `cpu_model`, `gpu_count`, `gpu_model`,
    `disk_mount`, `memory_total`, `memory_available`, `disk_total`, `disk_free`, `load1`, `load5`, `load15`,
    `uptime_seconds`, `tools`.
  - Addresses are taken in the order `tailnet_ip`, `tailnet_name`, `lan_ip` (`:102-108`).
- **L9, tachyon access probe:** the status only. 401 or 403 means denied; any other status means reachable
  (`accessstatus.go:192-225`). The write probe POSTs L7 `reload` on the id `tachyon-access-probe` and reads only
  the status.

## 3. Status codes and errors

| Case | Status | Body |
|---|---|---|
| No key or a bad key | 401 | `{"code":"unauthorized","message":…}` |
| Key lacks the route's scope | 403 | `{"code":…,"message":…}` from the auth guard |
| Bad `X-Cerberus-Api` | 400 | `{"code":"unsupported_api_version","message":"unsupported X-Cerberus-Api: <got> (server expects v1)"}` |
| Unknown resource on L3 to L6 | 404 | `{"code":"not_found","message":…}` |
| Unknown action under `/resources/{id}/` | 404 | `{"code":"not_found","message":"unknown resource action \"<action>\""}` |
| Unknown host | 404 | `{"code":"not_found","message":…}` |
| Bad `label` filter on L9 | 400 | `{"code":"invalid_args","message":"a label filter is key=value"}` |
| Wrong method | 405 | `{"code":"method_not_allowed","message":"method not allowed"}` |
| A mutation while the new build is in observe mode (until slice 5 and the cutover hand-over) | 409 | `{"code":"observe_mode","message":…}` |
| Gated refusals on L6 to L8 (lockdown, policy, approval) | as before: 423, 403, 428, 409 | `{"code":<old code>,"message":…,"details":{"approval":…}}` when an approval is named |

**Changed:** errors use Substrate's flat body `{code, message, details?}` instead of the old
`{success:false, error, …}`. The old connector codes keep their names and HTTP statuses
(`connector_error_wire.go:33-57` on `main`). Effects on callers:
- **tachyon:** none. It passes a non-2xx body through as `StatusError.Body` (`cerbcontrol.go:150-151`). Its
  console proxy shows that body under `cerberus` (`register_cerbcontrol.go:67-93`).
- **sysop:** on a status of 400 or above it reads `error` (`main.go:5414-5421`). With the new body it would fall
  back to its generic `"cerberus socket … http %d"` message. Reading `message` is part of CW-20261114-0145.
  Mutation results that are 200 with `success:false` keep `success` and `error` (L7), so sysop's main
  failure check is unaffected.

## 4. Keys for the clients

| Client | Scopes | Why |
|---|---|---|
| tachyon | `cerberus.read`, `cerberus.operate`, `cerberus.admin` | L1 to L9. It keeps `remove` (L8) for parity (director, 2026-11-14); narrowing the key is a post-adoption step (`docs/conversion-notes.md`) |
| tether sysop | `cerberus.read`, `cerberus.operate` | L3, L7 (`status`, `reload`, `apply`, `deploy`) |

No client key holds `cerberus.approve`; the server refuses it on any key (decision 0.1).

## 5. Not in the set

- `/ping`, `/whoami`, `/hosts/{name}` and the `/hosts/_…` routes; the plan, confirm and break-glass forms of the
  mutations; streaming (`X-Cerberus-Progress`). No current caller uses them. The console uses its own routes
  (`console-api-contract.md`).
- The CLI fallbacks. sysop runs `cerberus resource <action> <id>` and `cerberus register <file>`
  (`main.go:5345-5356, 5424-5484`), and tachyon's approve-and-run path runs `cerberus resource <verb> <id> --ack`
  and `cerberus resource list -o json` over SSH (`internal/actionrun/registry.go:67-72`,
  `internal/actionrun/manager.go:460-488`). These are CLI contracts (plan section 6), kept by the new CLI.
- sysop's self-heal reads `<adopt_root>/registry.yaml` (`entries[].owner`, `entries[].path`) directly
  (`main.go:5424-5484`). The file stays in place in the old format (slice 2).

## 6. Differences a client may notice

1. Errors use the flat body (section 3).
2. Observe mode: until the new build supervises (slice 5 and the cutover's hand-over step), L7 and L8 answer
   `409 observe_mode`. tachyon's access probe still reports "reachable".
3. L1's `resources[]` and the runtime fields of L2 to L5 (`status`, the `systemd_*` and `launchd_*` fields) come
   from the new build's observation of the same units. The values are the same kind; they are read by a
   different process.

   *Note (observe mode, not a contract change):* until supervision moves to the new build (slice 5), it reports
   what observation gives. These `omitempty` fields stay absent:
   - in L2: `operator_stopped`, `artifact_installed`, `artifact_stale`, `recommended_action`;
   - in L3: the `systemd_*` and `launchd_*` detail, the `artifact_*` fields and the dependency and config
     warnings;
   - in L4: the resolved command, paths and raw unit text;
   - in L5: the checks (`checks` is `[]`, and `summary` says observe mode);
   - in L9: `status` (the host monitor arrives with slice 8).

   The shapes and field names are unchanged.
4. Paths in L3 to L6 (`log_path`, `install_root`, `artifact_path`, unit paths) are unchanged, because the install
   root is adopted in place.

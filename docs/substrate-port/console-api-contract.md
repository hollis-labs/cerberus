# Console API contract (Substrate port)

Task: CW-20261114-0109, slice 11a of the port plan (`docs/substrate-port-plan.md` sections 4.3, 6
and 11). Status: **proposed**. The builder slices (4 to 7) implement the server side. The console in
`web/` is built against it. Change it by PR, never by an unannounced server change.

Inputs:
- `docs/substrate-port/inventory-gui.md` (every page and the calls it makes today);
- `hollis-labs/substrate` `docs/adopting-substrate.md` section 8 and `docs/identity.md`.

## 1. Conventions

| Rule | Value |
|---|---|
| Base path | `/api/`, same origin as the console. The process listens on `127.0.0.1:4785` (plan section 1) |
| Authentication | Substrate `identity`: an `HttpOnly`, `SameSite=Strict` session cookie set by `POST /api/login`. API keys (`Authorization: Bearer key_…`) for machine callers |
| Writes | Every non-GET carries `Content-Type: application/json` and `X-Requested-With: XMLHttpRequest`, and a cookie-authenticated write must have a same-origin `Origin` (`identity.Middleware`). **Removed:** `X-Cerberus-Web-Token`, `X-Cerberus-Session-Key`, the `#cerberus-key` fragment |
| Errors | The flat body `{"code": "…", "message": "…", "details": {…}?}` on every non-2xx. `401 unauthorized` when signed out. `403` when the scope is missing. `403 user_required` on person-only routes called with a key |
| Gated-write refusal | Same flat body. `code` is `approval_required`, `approval_pending` or `plan_stale`, and `details.approval` is `{id, expires_at?, approve_with?, channel}`. `channel: "tty_confirm"` means the console can confirm in place (plan then confirm); `"out_of_band"` means a passkey approval |
| Success bodies | Unchanged from today: the shapes in `web/src/api/types.ts` (carried from the old client). In-band `error` fields that some reads use for partial results stay. A request failure is never reported in a 200 |
| Path ids | URL-encoded single segments |

### Gated writes

A write that policy may gate has four forms on one base path. The base path is the same as today's:

| Form | Body | Answer |
|---|---|---|
| `POST <path>` | the write's body (`acknowledged` where the action has one) | the result, or a refusal naming an approval |
| `POST <path>/plan` | the same body | `ConnectorPlan` `{plan_hash, computed_by, plan}` |
| `POST <path>/confirm` | the body plus `{approval_id, confirmed_plan_hash}` | the result, or `409 plan_stale` |
| `POST <path>` (retry) | the body plus `{approval_id}` after an out-of-band approval | the result |

All four forms need the write's own scope. Confirming in place needs nothing more. An out-of-band
approval is decided by someone holding `cerberus.approve` (section 3.4).

## 2. Scopes

From the plan (section 4.3):

| Scope | Covers | Who may hold it |
|---|---|---|
| `cerberus.read` | every GET below | people, API keys, MCP stdio (local trust) |
| `cerberus.operate` | resource actions except remove, pipeline runs, brakes | people, API keys, MCP stdio |
| `cerberus.admin` | connector operations, credentials, plugins, registry writes, config restore, resource remove | people, API keys, MCP stdio |
| `cerberus.approve` | deciding approvals, revoking grants, passkey enrolment and removal | **a person's console session only**. The server refuses it on any API key, any token minted from a key, and every local-trust principal (route returns `403 user_required` for non-persons; the plan's scope test asserts it) |
| `authenticated` | `GET /api/identity/profile`, the identity self-service routes | any signed-in caller |
| `public` | `/healthz`, `GET /api/setup`, `POST /api/login` | anyone |

The owner holds `*`. The access-keys page offers only read, operate and admin.

## 3. Routes

**W** marks a write. "Gated" means the four forms in section 1.

### 3.1 Identity (Substrate-owned, used through kit-identity)

| Method | Path | Scope | Notes |
|---|---|---|---|
| GET | `/api/setup` | public | `{setup_required, setup_over_http:false}`. Setup is offline only (`cerberus identity setup`) |
| POST **W** | `/api/login` | public | `{username, password}`, sets the cookie |
| POST **W** | `/api/logout` | authenticated | ends the session. Replaces today's `POST /api/logout` with the action token |
| GET | `/api/identity/profile` | authenticated | who is signed in |
| PUT **W** | `/api/password` | authenticated | |
| GET, DELETE **W** | `/api/identity/sessions[/{id}]` | authenticated | |
| GET, POST **W**, DELETE **W** | `/api/identity/keys[/{id}]` | `cerberus.admin` | identity's `<app>.admin`; the server refuses `cerberus.approve` on any key |

### 3.2 Console status

| Method | Path | Scope | Response | Notes |
|---|---|---|---|---|
| GET | `/api/console/status` | `cerberus.read` | `ConsoleStatus` `{posture?, passkeys?, break_glass?, enforcement?, brakes?}` | **New.** The header badges and brakes banner, which today come once from `GET /api/session`. Polled every 15 s. `/api/session` is removed |

### 3.3 Pages

| Page | Method | Path | Scope | Request | Response |
|---|---|---|---|---|---|
| overview | GET | `/api/overview` | read | | `OverviewInfo` |
| overview | GET | `/api/system` | read | | `SystemInfo` |
| resources, connectors | GET | `/api/resources` | read | query `project?`, `connector?`, `tag?` | `ResourceInfo[]` |
| resources | GET | `/api/resources/{id}` | read | | `ResourceRuntimeStatus` |
| resources | GET | `/api/resources/{id}/logs` | read | query `stream`, `lines` | `LogLines` |
| resources | POST **W** gated | `/api/resources/{id}/{apply\|deploy\|reload\|stop\|sync}` | operate | `{acknowledged}` | `OpResult` |
| resources | POST **W** gated | `/api/resources/{id}/remove` | admin | `{acknowledged}` | `OpResult` |
| projects | GET | `/api/projects` | read | | `ProjectInfo[]` |
| pipelines | GET | `/api/pipelines` | read | | `PipelineInfo[]` |
| pipelines | POST **W** gated | `/api/pipelines/{id}/run` | operate | `{acknowledged}` | `PipelineRunResult` |
| registry | GET | `/api/registry` | read | | `RegistryListResponse` |
| registry | GET | `/api/registry/health` | read | | `RegistryHealthResponse` |
| registry | GET | `/api/config/validate` | read | | `ConfigValidationResponse` |
| registry | GET | `/api/config/resolve` | read | | `ConfigResolveResponse` |
| registry | POST **W** gated | `/api/registry/register` | admin | `{path}` | `{success, count}` |
| registry | POST **W** gated | `/api/registry/deregister` | admin | `{owner}` | `{success}` |
| credentials | GET | `/api/credentials` | read | | `CredentialsResponse` (presence only, never values) |
| credentials | POST **W** gated | `/api/credentials/{id}` | admin | `{secrets?, clear_secrets?}` | `CredentialSaveResult` |
| connectors | GET | `/api/connectors` | read | | `ConnectorDefinition[]` |
| connectors | POST **W** gated | `/api/connectors/{id}/operations/{op}` | admin | `ConnectorOperationRequest` `{config?, dry_run?, acknowledged?}` | `ConnectorOperationResult` |
| plugins | GET | `/api/plugins/connectors` | read | | `ManagedPluginState[]` |
| plugins | GET | `/api/plugins/connectors/{id}/health` | read | | `PluginHealth` |
| plugins | POST **W** gated | `/api/plugins/connectors/{id}/{load\|unload}` | admin | `{}` | `ManagedPluginState` |
| settings | GET | `/api/settings` | read | | `SettingsInfo` |
| settings | GET | `/api/config/backups` | read | | `ConfigBackupResponse` |
| settings | POST **W** gated | `/api/config/backups/restore` | admin | `{backup_path}` | `ConfigRestoreResponse` |
| approvals | GET | `/api/approvals` | read | | `ApprovalListResponse` |
| approvals | GET | `/api/approvals/{id}` | read | | `ApprovalInfo` (an approval link's target) |
| approvals | GET | `/api/approvals/keys` | read | | `PasskeyStatus` |
| every page (header) | POST **W** | `/api/brakes/lockdown` | operate | `{reason}` | `{state: BrakeState}`. Never gated: engaging the brake is always one click |
| header, approvals | POST **W** | `/api/brakes/lockdown/lift` | operate | `{approval_id}` | `{state: BrakeState}`. Refused until an out-of-band approval exists, then retried with it |
| header, approvals | POST **W** | `/api/brakes/freeze/{id}/lift` | operate | `{approval_id}` | `{state: BrakeState}`, same as lockdown lift |
| header | POST **W** | `/api/brakes/suspensions/{id}/reset` | operate | `{typed: "reset <id>"}` | `{state: BrakeState}` |

### 3.4 Approve (passkey ceremonies, slice 11c)

All of these are `cerberus.approve`, person-only (`403 user_required` otherwise), and need an
`Origin` the passkey service allows (section 4).

| Method | Path | Request | Response |
|---|---|---|---|
| POST **W** | `/api/approvals/{id}/challenge` | `{}` | `PasskeyCeremony` `{ceremony, options}` |
| POST **W** | `/api/approvals/{id}/decide` | `{approve, typed, reason, assertion?: {ceremony, credential}}`. An out-of-band approve must carry the assertion; approve needs the typed target | `ApprovalInfo` |
| POST **W** | `/api/approvals/{id}/revoke` | `{reason}` | `ApprovalInfo` |
| POST **W** | `/api/approvals/keys/register/begin` | `{token, label}`. `token` is the enrolment token from the host-side `cerberus approvals enroll` (polkit or Touch ID) | `EnrollBegin` `{ceremony, creation, authorize?}` |
| POST **W** | `/api/approvals/keys/register/finish` | `{ceremony, credential, authorize?}` | `{fingerprint}` |
| POST **W** | `/api/approvals/keys/remove/begin` | `{fingerprint}` | `PasskeyCeremony` |
| POST **W** | `/api/approvals/keys/remove/finish` | `{ceremony, credential}` | `PasskeyStatus` |

## 4. Passkeys and approval links

- **RP ID stays `localhost`** (plan, slice 11 brief), so keys enrolled on the old local console keep
  working. The allowed origins are an explicit setting (proposed key `cerberus.passkeys.allowed_origins`,
  a list; slice 6 names it), no longer derived from `~/.cerberus/web/login-*.key`. The default is the new listener's
  origin, `http://localhost:4785`. A key's credential is bound to its RP ID, and moving the console to
  a host name other than `localhost` makes every key unusable there.
- `keys.json` and `key-counters.json` move byte for byte with the audit chain (plan section 5),
  otherwise the 24-hour cooldown starts.
- **Approval links:** today an approval link signs a browser into a session limited to one approval
  (no nav, no brakes). Substrate identity has no scoped session. The new link is the deep link
  `/approvals?id=<approval>`. It shows the identity sign-in if needed, then the approvals page with
  that approval in front. Deciding still needs `cerberus.approve` and the passkey, so the safety
  property (a human with the passkey) holds. What is lost is the link working without a full sign-in.

## 5. Changes from today's console API

| Today | New | Why |
|---|---|---|
| One-time sign-in link, cookie plus `X-Cerberus-Session-Key` | identity session cookie (`POST /api/login`) | Substrate identity |
| `X-Cerberus-Web-Token` action token on writes | `X-Requested-With` plus same-origin `Origin` | Substrate CSRF rule |
| `GET /api/session` (token, scope, badges) | `GET /api/console/status` (badges only). Who-am-I is `GET /api/identity/profile` | token and scoped sessions gone |
| `{success:false, error, message, code?, approval?}` | `{code, message, details?}`, with `details.approval` | flat errors |
| Scoped approval session (`scopedRouteAllowed`) | deep link plus full sign-in | identity has no scoped sessions |
| Plugin load and unload were plain posts | gated like every other admin write | consistency. The server may never gate them, and then only the base form is used |
| `GET /login?token=` | removed. `cerberus web open` is retired; the owner signs in with a password | Substrate identity |
| Unused by the console and left to their own slices: `/api/brakes` GET, `/api/resources/{id}/{inspect,doctor}`, `/api/health`, `/api/portfolio/health`, `/api/plugins/connectors/{id}/operations/{op}`, `/api/schedules/v1/*`, `/api/hosts*`, `/api/components*` | not part of this contract | service and bearer clients (plan slices 7 to 9) |
| Retired 410 routes (`/api/config/migrate*`, `/api/plugins/connectors/{health,operations/,install}`) | not carried | already retired |

## 6. Open points

1. **Sign-in uses kit-identity.** The brief says identity has no UI kit. `@hollis-labs/kit-identity`
   0.1.4 is pinned in the BOM `kits:` and loom ships with it (`IdentityProvider`, `IdentityGate`,
   account, keys, sessions, sign-out). The scaffold uses it instead of an app-owned sign-in page.
   Reversible if the cell prefers app-owned.
2. **Header region.** app-shell 0.11.0 has no header or banner region. The status badges and the
   brakes banner render at the top of every page body (`ConsoleHeader` in `web/src/pages/common.tsx`).
   A Substrate gap is worth filing if the cell wants it in the shell.
3. **`cerberus.approve` on an owner key.** The owner holds `*`, and identity lets a holder grant any
   scope it holds. The server must refuse `cerberus.approve` (and `*`) on keys explicitly. This is the
   scope test in plan section 4.3.
4. **Key management scope.** `docs/identity.md` gives user and key administration to `<app>.admin`,
   so `cerberus.admin` manages keys. A holder of `cerberus.admin` without `*` cannot grant
   `cerberus.approve`, because identity refuses grants of scopes the granter does not hold.

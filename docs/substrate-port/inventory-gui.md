# Cerberus web console inventory (Substrate port)

Task: CW-20261114-0091 (mission CW-20261112-0099). Read from `main` at 493a9f0,
2026-11-14. No UI was changed and no console was restarted. This feeds the GUI part
of the port plan. File references are repo-relative. Observations of the live operator
host (the unit file, the public host name, which state files exist) are in Tesseract
workspace item `01M7DX6SZYHRC0RZV8X5G1922V` (`project/substrate-migration/workspace/cerberus`,
key `inventory-gui-full`).

## 1. How the console is built and served

| Item | Value |
|---|---|
| Stack | React 19, Vite 8, Tailwind 4, TypeScript, Biome, lucide-react (`web/package.json`) |
| UI library | `@hollis-labs/sysop-ui` 0.9.0: router (`createRouter`), `NavRail`, `PageHeader`, `ThemeSwitcher`, `Toaster`, API client and `ApiError`, theme tokens (`web/src/index.css`) |
| Build output | `vite.config.ts` builds to `internal/webui/dist` and restores `dist/.gitkeep` |
| Embed | `internal/webui/server.go:30` `//go:embed all:dist`, served through `fs.Sub` (`:41`) |
| Process | `cerberus web --listen 127.0.0.1:4783 --open=false`, a subcommand of the main binary (`cmd/cerberus/cmd_web.go:26`). It is a separate process from the daemon and reaches the daemon over `~/.cerberus/cerberus.sock` |
| Supervision | a user unit may run `cerberus web`; no code in this repository writes one. The operator host's details (unit, options, public host) are in Tesseract workspace item `01M7DX6SZYHRC0RZV8X5G1922V` |
| Public access | optional HTTPS reverse proxy; config key `web.public_url` (`internal/config/web.go:15`, `internal/webui/public_url.go`). With it set, cookies are `Secure` |
| Dev proxy | Vite proxies `/api` to `127.0.0.1:4783` and rewrites `Origin` |
| Tests | No frontend or browser tests. Go tests in `internal/webui/*_test.go` cover routes, guards, sessions, scoped sessions, passkeys and redaction |

## 2. Routes and session states (`web/src/App.tsx`)

Routes (`App.tsx:19-51`): `/` overview, `/resources`, `/projects`, `/pipelines`,
`/approvals`, `/registry`, `/credentials`, `/connectors`, `/plugins`, `/settings`
(nav-rail footer). Ten pages.

`GET /api/session` runs once on mount (`App.tsx:61-86`) and picks one of four states:

| State | Trigger | Renders |
|---|---|---|
| checking | request in flight; also stuck here on a non-401 error | nothing |
| signed-out | 401 | "run `cerberus web open`" (`:110-122`) |
| scoped | `info.scope` set (an approval link) | banner + sign-out + `<ApprovalsPage scoped/>` only; no nav, no brakes (`:91-108`) |
| signed-in | otherwise | full console: `NavRail` + `PageHeader` badges + `BrakesBanner` + routed page, inside `ConfirmOnCallProvider` |

Header badges (posture, passkeys alert, break-glass alert, enforcement, lockdown button
and brakes banner) all come from that one `/api/session` response (`server.go:239-242`)
and are not refreshed. Brake actions reload the page.

## 3. Pages

R = read, W = write. Every W sends the write guard headers (§5). "Confirm" means the
`useConfirmOnCall` / `useConsoleWrite` flow (`components/plan-confirm.tsx:81-135`):
send the call; on a `tty_confirm` refusal POST `…/plan`, show the plan and hash, make
the operator type the target, POST `…/confirm` with `{approval_id, confirmed_plan_hash}`;
on an `out_of_band` refusal poll `GET /api/approvals` every 2 s, approve in place with a
passkey, then retry with `approval_id`. Most pages also `GET /api/session` to read the
action token.

| Page (file) | Purpose | Calls |
|---|---|---|
| overview (`pages/overview.tsx`) | counts and 24h trends | R `GET /api/overview` (poll 5 s, `:43`), `GET /api/system` (5 s, `:44`) |
| resources (`pages/resources.tsx`) | resource list, quick actions, detail with logs | R `GET /api/resources` (5 s, `:56`), `GET /api/resources/{id}` (`:514,:555`), `GET /api/resources/{id}/logs?stream&lines=120` (`:640`). W `POST /api/resources/{id}/{apply\|deploy\|reload\|stop\|sync\|remove}` body `{acknowledged, approval_id?}` after a local ActionConfirm dialog, then Confirm via `…/plan` and `…/confirm` (`:239-241`, `:549-551`) |
| projects (`pages/projects.tsx`) | project list | R `GET /api/projects` (5 s, `:70`) |
| pipelines (`pages/pipelines.tsx`) | pipeline list and run | R `GET /api/pipelines` (5 s, `:11`). W `POST /api/pipelines/{id}/run` after ActionConfirm (effect `exec`), Confirm via `/run/plan`, `/run/confirm` (`:59-62`) |
| approvals (`pages/approvals.tsx`) | approvals, grants, passkey panel (panel hidden when scoped) | R `GET /api/approvals` (5 s, `:16`; server filters to the scope), `GET /api/approvals/keys` (15 s, `:127`). W passkey enroll `POST /api/approvals/keys/register/begin` → `register/finish` (WebAuthn create, plus an assertion from an existing key when one must authorize, `:148-151`); passkey remove `POST /api/approvals/keys/remove/begin` → `remove/finish` (assertion, `:157-159`) |
| registry (`pages/registry.tsx`) | registry, health, validation, resolve; register and deregister | R `GET /api/registry` (5 s, `:86`), `/api/registry/health` (`:87`), `/api/config/validate` (`:88`), `/api/config/resolve` (`:89`). W `POST /api/registry/register` `{path}` and `POST /api/registry/deregister` `{owner}`, each Confirm via `/plan`, `/confirm` (`:144`, `:159`) |
| credentials (`pages/credentials.tsx`) | per-connector secrets editor (presence only, never values) | R `GET /api/credentials` (5 s, `:12`). W `POST /api/credentials/{id}` `{secrets}`, Confirm (`:56`) |
| connectors (`pages/connectors.tsx`) | connector definitions, operation runner | R `GET /api/connectors` (5 s, `:18`), `GET /api/resources` (15 s, `:19`). W `POST /api/connectors/{id}/operations/{op}` `{config, dry_run, acknowledged, approval_id?}`, Confirm (`:73-76`) |
| plugins (`pages/plugins.tsx`) | managed plugins, health, load and unload | R `GET /api/plugins/connectors` (5 s, `:7`), `GET /api/plugins/connectors/{id}/health` (`:27,:87`). W `POST /api/plugins/connectors/{id}/load` and `/unload`, plain call with no Confirm flow (`:65,:67`) |
| settings (`pages/settings.tsx`) | config summary, backup restore | R `GET /api/settings` (5 s, `:46`), `GET /api/config/backups` (5 s, `:47`). W `POST /api/config/backups/restore` `{backup_path}`, Confirm (`:97`) |

Shared components that call the API:

| Component | Calls |
|---|---|
| `components/approval-detail.tsx` | W `POST /api/approvals/{id}/decide` (approve needs the typed target; deny, `:111,:124`); out-of-band approve `POST /api/approvals/{id}/challenge` → WebAuthn assertion → `POST /api/approvals/{id}/decide` `{assertion}` (`:112-114`); `POST /api/approvals/{id}/revoke` (`:146`); "Lift now" `POST /api/brakes/lockdown/lift` or `/api/brakes/freeze/{id}/lift` with `approval_id` (`:131-143`) |
| `components/brakes.tsx` | W `POST /api/brakes/lockdown` `{reason}` (no confirm by design, `:84`); `POST /api/brakes/lockdown/lift` and `/api/brakes/freeze/{id}/lift` with in-place passkey approval (`:35,:47`); `POST /api/brakes/suspensions/{id}/reset` `{typed}` (`:129`) |
| `components/plan-confirm.tsx` | R `GET /api/approvals` (2 s while waiting, `:214`), `GET /api/session` (`:223`) |
| `components/resolve-notice.tsx` | R `GET /api/registry` (15 s, `:17`) |
| `action-confirm.tsx`, `plan-body.tsx` | none (presentation) |
| `src/webauthn.ts` | none; `createPasskey` / `assertPasskey` wrap `navigator.credentials` with `PublicKeyCredential.parse*OptionsFromJSON` |

Sign-out everywhere: `POST /api/logout`.

Server routes the console never calls: `GET /api/brakes`, `GET /api/approvals/{id}`,
`GET /api/resources/{id}/{inspect,doctor}`, `GET /api/health`, `/api/portfolio/health`,
`POST /api/plugins/connectors/{id}/operations/{op}`, `/api/schedules/v1/*`,
`/api/hosts*`, `/api/components*` (bearer and service clients), and the 410 retired
routes (`/api/config/migrate[/preview]`, `/api/plugins/connectors/{health,operations/,install}`).
Route table: `internal/webui/server.go:150-199`.

## 4. Sign-in, sessions and where they live

There is no password and no account. Sign-in is a one-time link minted on the host.

- **Login key file**: `~/.cerberus/web/login-<listen-addr>.key` (`session.go:375-377`),
  JSON `{url, key, ttl}`, written atomically at start and removed on shutdown
  (`:383`). The key is random per process (`session.go:95-100`). `cerberus web open`
  reads it and mints an HMAC login token (`MintLoginURL`, `:460`).
- **Login**: `GET /login?token=…` (`session.go:281`) redeems the token (TTL 2 min,
  `:59`) and sets cookie `cerberus_session_<port>` (`HttpOnly`, `SameSite=Strict`,
  `Secure` when a public URL is set, `:296-299`). It also returns a bind key in the URL
  fragment `#cerberus-key=`, which the page keeps in localStorage
  (`cerberus.session-key`) and sends as `X-Cerberus-Session-Key` (`web/src/api/client.ts:8-48`).
  A cookie without its bind key is not a session (`session.go:194-209`).
- **Session store**: in memory only, `map[sha256(cookie)]*session` (`session.go:88`),
  idle 30 min, max 12 h (`:61-63`). Restarting the console signs everyone out. **No
  session data to migrate.**
- **Scoped sessions**: `cerberus` mints an approval link (`MintApprovalURL`, `:477`)
  whose session may only reach the approval routes in `scopedRouteAllowed`
  (`approvals.go:68-77`); others return 403 `scoped_session`.
- **Action token**: each session has an `action_token`, returned by `GET /api/session`
  and required on every write as `X-Cerberus-Web-Token`.

## 5. Write guard and error shape

`allowStateChangingRequest` (`server.go:368-397`): `Content-Type: application/json`;
a session whose `action_token` matches `X-Cerberus-Web-Token` (constant time); an
allowed `Host`; `Origin` absent or allowed. A bearer token (hosts and components routes
only) skips the browser checks. Before that, `guard.Middleware` rejects bad Host or
Origin on every request (`internal/webui/loopback.go:190-202`) and `requireSession`
answers 401 `login_required` (`session.go:243-276`). Passkey ceremonies require an
`Origin` (`passkeys.go:35-38`).

Errors (`server.go:495-509`): `{"success":false,"error":msg,"message":msg[,"code":…]}`.
Connector refusals add `approval: {id, expires_at, approve_with, channel}`
(`server.go:522-529`), which drives the Confirm flow (`client.ts:635-644`). Loopback
rejections send only `{"success":false,"error":msg}` (`loopback.go:204-207`).

## 6. WebAuthn passkeys and approval state (data to migrate)

Passkeys approve actions out of band. They are not a sign-in method. The daemon owns
them, not the console process (`cmd/cerberus/daemon_presence.go:35`).

| Data | Location | Notes |
|---|---|---|
| Passkey registry | `~/.cerberus/approvals/passkeys/keys.json` (`internal/presence/presence.go:59`) | `{version, user_id, keys:[{fingerprint, label, enrolled_at, credential}]}`; `credential` is a go-webauthn `Credential` (public key, sign count, flags) |
| Sign counters | `~/.cerberus/approvals/passkeys/key-counters.json` (`presence.go:60`) | clone detection |
| Approvals and grants | `~/.cerberus/approvals/events.jsonl` (`internal/approval/store.go:101,115`) | append-only event log; state is replayed from it |
| Enrollment evidence | `~/.cerberus/audit/YYYY-MM.jsonl` (`internal/audit/sink.go:135`) | hash-chained. At start the daemon checks the registry against the audit log's enrollment records (`daemon_presence.go:24-33`) |
| Brakes | `~/.cerberus/brakes/events.jsonl` | lockdown, freeze, suspension state |

Constraints the migration has to respect:

1. **Relying party ID is the console hostname** (`presence.go:307-319`): `localhost`
   for local consoles (`RPID`, `:52`), the public host for a proxied console.
   Allowed origins come from the running consoles' login key files
   (`ConsoleOrigins`, `session.go:552-571`). A passkey only works on the hostname it was
   enrolled on, so the new console must serve the same host(s), or every key has to be
   re-enrolled.
2. **A registry changed outside Cerberus starts a 24 h cooldown** on out-of-band
   approvals (`Cooldown`, `presence.go:54-56`), and the registry is verified against the
   hash-chained audit log. Copying `keys.json` without the matching audit records, or
   rewriting it, refuses approvals for a day. The registry and the audit chain have to
   move together and byte-for-byte, or the new app has to import them with an audited
   record.
3. **Enrollment needs host authentication** (polkit on Linux, Touch ID or password on
   macOS via `cerberus-presence`; `daemon_presence.go:45-60`). Re-enrollment cannot be
   done from the browser alone.

## 7. Mapping to Substrate GUI conventions

Reference: substrate `docs/adopting-substrate.md` §8 and §11, `docs/identity.md`,
`docs/writing-a-module.md` ("Routes and UI"); the wave-1 port of loom
(`frontend/CONVERSION_NOTES.md`).

**Maps directly**

| Cerberus today | Substrate |
|---|---|
| Vite SPA embedded with `//go:embed all:dist` in `internal/webui` | same: `httphost.Options{Assets}` from `internal/webui` with `all:dist` |
| 10 nav-rail pages at `/<name>` | `ui/app.json` `nav.item` + `surface` pairs (`module: "cerberus"`), rendered by app-shell `ManifestShell`; settings as `nav.footer` |
| list pages with 5 s polling (overview, resources, projects, pipelines, registry, connectors, plugins) | design-components / kit-dashboard (Card, Metric, Table, Pill, EmptyState), surface layout `list` |
| settings page | kit-settings (`settings.region`) |
| sysop-ui theme tokens | design-tokens |
| cookie `HttpOnly` + `SameSite=Strict` | same in Substrate identity |

**Needs work (decide in the port plan)**

1. **Identity model.** Cerberus has no account or password; sign-in is a host-minted
   one-time link with a cookie plus a bind key in the URL fragment. Substrate identity
   (kit-identity: owner, password, keys, sessions) is a different model. Options: adopt
   Substrate identity and keep `cerberus web open` as a way to mint a session, or keep
   the link model and leave kit-identity out. This is the first contract to agree with
   the builder.
2. **CSRF header.** `X-Cerberus-Web-Token` (per-session action token) versus
   Substrate's `X-Requested-With` plus same-origin `Origin`. The new client follows
   whatever the backend contract says.
3. **Error body.** `{success, error, message, code?, approval?}` versus Substrate's flat
   `{code, message, details?}`. The `approval` refusal payload has to survive, probably
   as `details.approval`, because the Confirm flow depends on it.
4. **Plan / confirm / out-of-band approval flow** (`plan-confirm.tsx`,
   `approval-detail.tsx`, `brakes.tsx`). No kit covers it. It stays app code, with
   `risk` set on the manifest actions (`low|medium|high`).
5. **Passkeys** (`webauthn.ts`, approvals panel). No kit. App code, plus the RP ID and
   cooldown constraints in §6.
6. **Scoped approval sessions.** A session limited to one approval, with no nav. app-shell
   has no "shell-less" mode. Needs a bare route outside `ManifestShell`, or a Substrate
   gap.
7. **Header badges and brakes banner.** app-shell has no header-badge region seen so far.
   Candidates: a `widget` / `slot`, or a gap to file. Lockdown must stay one click.
8. **Surface routes must not equal backend paths.** Console pages are `/resources` etc.
   and the API is `/api/resources`, so they don't collide today. Keep that.
9. **No browser tests today.** The port adds a Playwright suite against the real
   binary on a throwaway home, as loom did.

Small existing bugs, recorded here and not fixed in this PR:
- In a scoped session ApprovalDetail shows Revoke and Lift now, which the server refuses
  with 403 (`approvals.go:72-74`).
- Plugin load and unload skip the Confirm flow, so a refusal that needs approval shows as
  a plain error.
- A non-401 error on `/api/session` leaves a blank page.

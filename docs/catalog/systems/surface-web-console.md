---
id: "CERB-CAP-403"
class: "capability"
name: "Web console"
summary: "A React console on 127.0.0.1:4783 over a 29-route JSON API — the only surface that renders every connector operation mechanically, and the only home of the overview trend and the infra-deployment subsystem."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.88
confidence_label: "routes, guard and connectors page re-read on main after P0 (#48 to #54); the non-loopback --listen refusal run live"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/webui/server.go"
tags:
  - "cerberus"
  - "surface"
  - "console"
  - "webui"
  - "http"
  - "area:surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "the console column in the surface matrix"
  - type: "depends_on"
    target: "CERB-CAP-401"
    note: "every handler is thin over the socket client"
  - type: "implements"
    target: "CERB-TOOL-414"
    note: "the generic connector operation runner"
  - type: "blocks"
    target: "CERB-GAP-439"
    note: "internal/infra is console-only"
  - type: "blocks"
    target: "CERB-GAP-440"
    note: "the overview trend is console-only"
  - type: "blocks"
    target: "CERB-GAP-441"
    note: "the embed breaks a fresh checkout"
  - type: "blocks"
    target: "CERB-GAP-442"
    note: "the action token is not authentication"
  - type: "blocks"
    target: "CERB-GAP-445"
    note: "the running console is a stale unmanaged process"
  - type: "relates_to"
    target: "CERB-DEC-472"
    note: "config migration is tombstoned with a 410 and a test"
  - type: "relates_to"
    target: "CERB-DEC-810"
    note: "the Host and Origin guard in front of every route"
  - type: "relates_to"
    target: "CERB-DEC-811"
    note: "--listen is loopback only"
---

# Web console

`cerberus web` serves a React SPA plus a JSON API on a loopback address
(default `127.0.0.1:4783`). Since PR #48 `--listen` refuses anything but
`localhost` or a literal loopback IP (CERB-DEC-811). The bundle is `go:embed`-ed from `internal/webui/dist`.

**It is the only surface that is mechanically complete for connectors.** The
Connectors page fetches `/api/connectors`, iterates `connector.operations` from
the discovery metadata, and renders for every operation a Run button, a
config input, a dry-run checkbox and an acknowledge checkbox (the latter two
disabled unless the metadata says `supports_dry` / `destructive`). The config
input is a free-form JSON box, except for ssh and docker, which since PR #54 get
a resource picker filtered by connector, an optional local container name for
docker, and one field per remaining schema input.
`POST /api/connectors/{id}/operations/{op}` forwards to the socket's generic
route. Because `ListConnectors` includes managed plugins, this renders Azure's
six operations and ContextForge's four as well. A new connector operation
appears in the console the moment the connector declares it — which is exactly
the property the MCP surface does not have.

It also owns capability that exists nowhere else:

- **`/api/overview`** — a 24-hour trend built from
  `~/.cerberus/state/overview_snapshots.json`, written by a recorder goroutine
  in the daemon on a one-minute tick. `Overview` appears in no CLI command, no
  socket route and no MCP tool.
- **`/api/infra`, `/api/deployments`** — a provider/deployment-profile subsystem
  backed by `~/.cerberus/infra.yaml`. `internal/infra` is imported by exactly one
  file, `internal/webui/infra.go`. It used to carry a hardcoded suggestion for
  another user's site path; PR #63 removed it and the suggestions feature with
  it.
- **`/api/settings`**, **`/api/config/backups`**, **`/api/registry/register`**
  and **`/api/registry/deregister`** — config-adjacent operations with CLI
  equivalents but no MCP tools.

Authorisation, since PR #48: every request, GETs included, first passes
`loopback.Guard`. It returns 403 unless `Host` is a loopback name or the listen
host, whatever the port, and, when an Origin is sent, unless it matches a
loopback origin on the listen port exactly (CERB-DEC-810). That closed DNS
rebinding. Before, a hostile page that re-pointed its own name at `127.0.0.1`
could read the token same-origin and pass the old Origin check, which compared
against the attacker-controlled `r.Host`. Mutating routes then go through
`allowStateChangingRequest`, which requires `Content-Type: application/json`, a
matching `X-Cerberus-Web-Token`, and the same exact Origin match when an Origin
is present. Routes are built from one `routeTable()`, and tests require every
route to be classified and every mutation to go through the guard. The token is
a per-process 32-byte random value handed out unauthenticated at
`GET /api/session`. Verified live at audit time: the token is readable with a
plain `curl`, and a token-less POST to a mutating route returns
`403 state-changing request rejected`. That is CSRF defence, now sound against
rebinding, and still not authentication against a local process
(CERB-GAP-442).

Two retired surfaces are tombstoned rather than deleted: `POST
/api/config/migrate` and `GET /api/config/migrate/preview` return `410 Gone`
with `centralized config migration is retired`, and
`TestCentralizedMigrationEndpointsAreGone` holds them there. PR #50 retired
three plugin routes the same way: install-by-path and the two `plugin_dir`
routes answer 410, and the plugins page shows the CLI install command
instead.

The build-order trap is real and worse than documented. `internal/webui/dist` is
gitignored and its `.gitkeep` placeholder was removed in `a54daa8`, so a fresh
clone or worktree has no `dist` directory at all and `//go:embed all:dist`
fails. Verified live in the audit worktree: `go vet ./internal/webui/` and
`go test ./cmd/cerberus` both fail with `pattern all:dist: no matching files
found`. The console does not embed a stale bundle — the module does not compile.

## Since P1 (PRs #57, #60 and #63)

**Refusals render.** Since PR #57 a connector refusal answers with its status
from the shared table (400, 404, 409, 422, 503; 502 for `operation_failed`
since PR #60), and the body carries `message`, which the console's API client
renders, and `code`.

**Every mutation asks first.** A shared `ActionConfirm` dialog (sysop-ui
`ConfirmDialog`) is the only thing in the console that sends
`acknowledged: true`, and it names the operation's effect. It fronts every
resource action (row quick actions and the detail dialog), pipeline Run and
deployment-profile Run. The Connectors page's Acknowledge checkbox follows the
operation's `requires_ack` and shows its effect.

**A deployment is confirmed against its plan.** The confirm dialog lists every
command a profile run will execute and the directory it runs in, from
`GET /api/deployments/{id}/plan` (CERB-TOOL-418). The Vercel token appears as
`VERCEL_TOKEN=<vercel token>` and reaches the child only in its environment,
never in argv or a shell string. The run is not yet bound to the plan it showed
(CERB-GAP-853).

The console marks every request as the `web` surface, so an in-process client
behind it refuses local-only inputs just as the daemon would.

## Since P2-1

Every console request is labelled `kind: human, via: web`, marked
self-reported, and nothing the browser sends is read as a claim. The console
passes that label on to the daemon, which verifies the uid of the web process,
not of whoever is at the browser. The label becomes real when P2-2 adds a login
(CERB-GAP-442).

## Since P2-2 (Decision 20)

**The console has a login.** `cerberus web` prints and opens a one-time URL.
`cerberus web open` mints another from the key the running console keeps at
`~/.cerberus/web/login-<listen>.key`, mode 0600 in a 0700 directory and removed
on shutdown. A token is an expiry, a nonce and an HMAC under that per-process
key. It is good for two minutes and one use. `GET /login` checks it, spends the
nonce, and sets `cerberus_session` (`HttpOnly`, `SameSite=Strict`, `Path=/`),
then answers 303 to `/` with `Cache-Control: no-store` and
`Referrer-Policy: no-referrer`, so the token leaves the address bar.

Every `/api/` route needs a live session, and one without answers 401
`login_required` with the command that signs in. A session ends after 30 minutes
idle (`--session-idle`), after twelve hours in any case, on
`POST /api/logout`, or when `cerberus web` exits, because keys, nonces and
sessions live only in memory. The SPA shows a sign-in screen on 401 and has a
Sign out control.

**The action token stays, per session.** `GET /api/session` serves a token
only to a signed-in session, and each session has its own. A state-changing
request needs the cookie *and* that session's token, as well as the JSON content
type and the Host and Origin guards. The cookie alone is not enough, for two
reasons. `SameSite` treats every port on `127.0.0.1` as one site, so a page
served by any other local web app is same-site and would carry the cookie. And
the Origin check passes a request that sends no `Origin`. The per-session token
is readable only by a page that can read `/api/session` as that session, which
same-origin policy confines to the console itself.

**The principal is the session.** A signed-in request carries
`kind: human, via: web, session: <public id>`. The id is never the cookie's
value. It reaches the daemon's audit record through the socket client's
principal claim, and the daemon adds the verified uid of the console process.

Verified in headless Chrome over CDP on a scratch `HOME`:

- signed out, the page shows the sign-in screen and `/api/session` is 401;
- the link signs in, and the cookie is `HttpOnly` and `Strict`, invisible to
  `document.cookie`;
- a reused link is refused;
- a POST without the session token is 403;
- Sign out clears the cookie and returns to the sign-in screen;
- a `cerberus web open` link signs in;
- after restarting `cerberus web`, the old session and the old link are both
  refused, and the key file is gone;
- a connector call made from the browser is audited as
  `{kind: human, via: web, session: …, uid_verified: true}`.

The login separates the operator's account from everything else, not a person
from an agent running as that account. Any same-uid process can mint a link,
recorded as CERB-GAP-862.

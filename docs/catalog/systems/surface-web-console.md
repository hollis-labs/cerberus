---
id: "CERB-CAP-403"
class: "capability"
name: "Web console"
summary: "A React console on 127.0.0.1:4783 over a 29-route JSON API — the only surface that renders every connector operation mechanically, and the only home of the overview trend and the infra-deployment subsystem."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.88
confidence_label: "API probed live against a console already running on 4783; UI behaviour read from web/src rather than driven in a browser"
last_reviewed: "2026-09-17"
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
---

# Web console

`cerberus web` serves a React SPA plus a 29-route JSON API on
`127.0.0.1:4783`. The bundle is `go:embed`-ed from `internal/webui/dist`.

**It is the only surface that is mechanically complete for connectors.** The
Connectors page fetches `/api/connectors`, iterates `connector.operations` from
the discovery metadata, and renders for every operation a Run button, a
free-form JSON config textarea, a dry-run checkbox and an acknowledge checkbox
(the latter two disabled unless the metadata says `supports_dry` / `destructive`).
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
  file, `internal/webui/infra.go`. It also carries a hardcoded suggestion for
  `/Users/chrispian/dev/sites/chrispian.dev`, another user's path, so on this
  machine it returns empty.
- **`/api/settings`**, **`/api/config/backups`**, **`/api/registry/register`**
  and **`/api/registry/deregister`** — config-adjacent operations with CLI
  equivalents but no MCP tools.

Authorisation: GET routes are open. Mutating routes go through
`allowStateChangingRequest`, which requires `Content-Type: application/json`, a
matching `X-Cerberus-Web-Token`, and — *only when an `Origin` header is
present* — a same-origin `Origin`. The token is a per-process 32-byte random
value handed out unauthenticated at `GET /api/session`. Verified live: the token
is readable with a plain `curl`, and a token-less POST to a mutating route
returns `403 state-changing request rejected`. That combination is CSRF defence,
not authentication.

Two retired surfaces are tombstoned rather than deleted: `POST
/api/config/migrate` and `GET /api/config/migrate/preview` return `410 Gone`
with `centralized config migration is retired`, and
`TestCentralizedMigrationEndpointsAreGone` holds them there.

The build-order trap is real and worse than documented. `internal/webui/dist` is
gitignored and its `.gitkeep` placeholder was removed in `a54daa8`, so a fresh
clone or worktree has no `dist` directory at all and `//go:embed all:dist`
fails. Verified live in the audit worktree: `go vet ./internal/webui/` and
`go test ./cmd/cerberus` both fail with `pattern all:dist: no matching files
found`. The console does not embed a stale bundle — the module does not compile.


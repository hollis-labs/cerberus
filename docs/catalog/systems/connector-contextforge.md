---
id: "CERB-CAP-209"
class: "capability"
name: "ContextForge connector (plugin)"
summary: "Reads the ContextForge MCP gateway's health, upstream registrations, virtual servers and tools — with the credentialed reads never yet run against a real gateway."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "No JWT on this machine; the three credentialed operations have only ever run against a fake"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "~/Projects-apps/cerberus-plugins/contextforge (plugin manifest)"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "contextforge"
  - "dto-allowlist"
  - "mcp-gateway"
  - "missing-secret"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Its operations dispatch through the admin lane"
  - type: "depends_on"
    target: "CERB-CAP-301"
    note: "Loaded at runtime; reports missing_secrets: [token]"
  - type: "implements"
    target: "CERB-DEC-293"
    note: "The ADR 0003 DTO allow-list exists because of this connector's vendor type"
  - type: "blocks"
    target: "CERB-GAP-287"
    note: "list_gateways has never run against a real gateway"
---

# ContextForge connector (plugin)

> Reads the ContextForge MCP gateway's health, upstream registrations, virtual servers and tools — with the credentialed reads never yet run against a real gateway.

Four operations: `get_health`, `list_gateways`, `list_virtual_servers`,
`list_tools`. All reads, none destructive, none dry-runnable.

The split between them is the design. `get_health` is open — no credential — and
must stay that way, because that is how you tell a down tunnel from a down
gateway: if health answers and `list_gateways` 401s, the tunnel is up and the
credential is wrong; if neither answers, the tunnel is down. The plugin loads
and reports `missing_secrets: ["token"]` rather than failing, so the open
operation keeps working while the credentialed ones do not. A missing credential
is not fatal, by design.

This connector is also why ADR 0003 exists. `go-contextforge`'s `Gateway` type
— the upstream MCP server registration — declares `AuthToken`, `AuthPassword`,
`AuthHeaderValue`, `AuthValue`, `AuthUsername`, `AuthHeaders` and `OAuthConfig`,
with 32 secret-bearing field references across the package's types. A
`list_gateways` that marshalled that struct would have emitted live upstream
credentials into CLI stdout, the daemon socket response, `~/.cerberus/` logs,
and — most consequentially — MCP tool results, which land in an agent's context
window and are then sent to a model provider. ContextForge gateways are, by the
team's own operational notes, the only place upstream auth can be set, so that
one payload carries the credential for every MCP server behind the gateway. The
severity is a property of the vendor type, not of the code: `return gateways,
nil` looks identical whether the struct holds a name or a bearer token. Nothing
in the compiler, the tests or the linter objects. So the connector maps to a
Cerberus DTO that is an allow-list, exposing `auth_type` — that some bearer auth
is configured — and never `auth_token`.

And the thing that has not happened: `list_gateways`, `list_virtual_servers` and
`list_tools` have **never run against a real gateway**. There is no JWT on this
machine. Their mapping is unit-tested against a fake, `get_health` works live,
and the credentialed path is unproven. Nothing is blocked on code — the host
secret channel shipped — so this is an operator action: obtain a JWT, store it
under `contextforge/token`, reload the plugin, and confirm the DTOs carry no
credential material from a gateway that really has `authToken` set. If any
response carries a credential, that is a defect in the allow-list and outranks
everything else in the connector plan.

## Owns

- Gateway health, unauthenticated and deliberately so
- Upstream MCP server registrations, mapped through a DTO allow-list
- Virtual server and published tool listings
- Reporting its own missing credential as missing_secrets rather than failing to load

## Does not own

- Any gateway write. Nothing registers, edits or removes an upstream
- The tunnel to the gateway. That is a local process resource with auto_start and auto_restart deliberately false
- The gateway's own credentials. It must never emit them, which is the point of the DTO
- Workday, the Teams bot or anything else running on the same host

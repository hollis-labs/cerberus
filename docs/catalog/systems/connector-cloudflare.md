---
id: "CERB-CAP-204"
class: "capability"
name: "Cloudflare connector"
summary: "Lists and creates Cloudflare zones and DNS records as the cloudflare plugin in hollis-labs/cerberus-plugins; no longer compiled in, which took a stripped build from 62.8MB to 23.6MB."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.8
confidence_label: "Fake-backend, DTO and scrubber tests in the plugin; no Cloudflare token on this machine, so live reads wait for the operator's UAT"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins:cloudflare/internal/cloudflareplugin/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "cloudflare"
  - "connector"
  - "dns"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "A plugin connector since 2026-09-25, reached through the admin lane"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "The first of the four to migrate, because it was most of the binary"
  - type: "relates_to"
    target: "CERB-GAP-280"
    note: "The built-in had no tests; the plugin has them"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "The plugin returns Cerberus DTOs; cloudflare-go is confined to one file"
---

# Cloudflare connector

> Lists and creates Cloudflare zones and DNS records as the cloudflare plugin in hollis-labs/cerberus-plugins; no longer compiled in, which took a stripped build from 62.8MB to 23.6MB.

Five operations: `list_zones` and `list_dns_records` are `read`,
`create_zone` and `create_dns_record` are `write`, and `delete_dns_record` is
`destructive`. The three writes are acknowledgment-gated and preview with a
`plugin` preview, which is the plugin's claim and not verified by the host.

Since 2026-09-25 this is a plugin, not a built-in
(`docs/plans/provider-plugin-extraction.md`, H4). The id and the secret name are
unchanged, so `CERBERUS_CLOUDFLARE_API_TOKEN`, a `cloudflare: api_token:` entry
in `connector-secrets.yaml` and `keychain://cloudflare/api_token` all resolve
exactly as before. The host looks a plugin's secret up as
`<plugin id>/<secret name>`, which is the key the built-in used. Removing it
took `cloudflare-go/v4` and its tidwall JSON dependencies out of the binary: a
stripped build went from 62.8MB to 23.6MB, and an unstripped one from 88.1MB to
34.1MB. The SDK's generated types weighed far more in type metadata and line
tables than their symbol sizes suggested.

What changed for a caller:

- **CLI.** `cerberus cloudflare …` is gone, with no tombstone.
  `cerberus connectors exec cloudflare <op>` runs any operation, with
  arguments typed from the schema.
- **MCP.** The hand-written `cerberus_cloudflare_zones`, `_zone_create`,
  `_dns_list`, `_dns_create` and `_dns_delete` are gone. The generated tools
  are `cerberus_cloudflare_<op>`, served only for the operations the operator
  lists under `cloudflare: mcp: expose:` in `connector-config.yaml`.
- **No wrangler fallback.** It authenticated outside the declared-secret
  channel and could not list or create zones.
- **Tighter than the built-in.** `create_dns_record` validates `type`,
  `name` and `content` on the real path, bad numbers are refused rather than
  defaulted, `delete_dns_record` returns `{deleted, zone_id, record_id}`
  rather than null, and health makes no network call.

The plugin carries what the built-in did not: fake-backend tests for every
operation, a DTO canary, a token scrubber with a sentinel test, coded errors
(`credential_missing`, `connector_unavailable`, `invalid_args`), and a
live-check script that requires a read-only token.

## Owns

- Zone list and create
- DNS record list, create and delete, with proxied, TTL and priority
- The Cloudflare API client, confined to one file behind a DTO-returning backend

## Does not own

- Tunnels, Workers, R2, or anything else in the Cloudflare product surface
- Registrar operations — that is namecheap
- Zone deletion. create_zone exists; there is no destroy_zone
- A place in the Cerberus binary

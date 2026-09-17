---
id: "CERB-CAP-204"
class: "capability"
name: "Cloudflare connector"
summary: "Lists and creates Cloudflare zones and DNS records through cloudflare-go, with the largest dependency in the binary and no test files."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.75
confidence_label: "No token on this machine and the package has zero test files, so nothing here is verified beyond its declaration"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/cloudflare/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "cloudflare"
  - "connector"
  - "dns"
  - "migrating-to-plugin"
  - "untested"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Compiled in today, scheduled to migrate out"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "First of the four to migrate, because the 33MB payoff is largest"
  - type: "relates_to"
    target: "CERB-GAP-280"
    note: "Zero test files in a package with three destructive operations"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "Returns cloudflare-go types rather than a Cerberus DTO"
---

# Cloudflare connector

> Lists and creates Cloudflare zones and DNS records through cloudflare-go, with the largest dependency in the binary and no test files.

Five operations: `list_zones`, `create_zone`, `list_dns_records`,
`create_dns_record`, `delete_dns_record`. Three are destructive and all three
are dry-runnable, with previews in the admin lane that echo the resolved target
and input before anything is sent.

The wrangler CLI backend exists but is thin to the point of being mostly
refusals: listing zones, creating zones and listing tunnels each return an error
telling the caller to use the API backend instead. In practice this connector is
the API client or it is nothing.

Two facts about it are more load-bearing than the operation list.

`cloudflare-go/v4` is 33MB and the single largest dependency win available in
the binary, which is why Cloudflare is named first in the migration order to
plugins. That is a decision already taken, deferred deliberately: the four
compiled-in provider connectors work today, moving them is cost with no feature
benefit, and it would mean migrating onto a plugin lane that had never carried a
plugin authored as one from day one. ContextForge was built first to learn what
authoring feels like.

And `internal/connector/cloudflare/` has **no test files**. Three destructive
DNS operations, a vendor SDK behind a `Backend` interface built precisely so it
could be tested without network, and nothing exercises it. `go test
./internal/connector/...` reports `[no test files]` for this package. The
dry-run previews and the service dispatch are covered from `internal/cerbapi`;
the connector itself is not. On this machine there is also no Cloudflare token,
so nothing here is verified beyond its declaration.

## Owns

- Zone list and create
- DNS record list, create and delete, with proxied, TTL and priority
- Two backends: the cloudflare-go API client and the wrangler CLI

## Does not own

- Tunnels, Workers, R2, or anything else in the Cloudflare product surface
- Registrar operations — that is namecheap
- Zone deletion. create_zone exists; there is no destroy_zone
- Its own tests. There are none

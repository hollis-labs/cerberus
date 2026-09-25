---
id: "CERB-CAP-207"
class: "capability"
name: "Namecheap connector"
summary: "Manages Namecheap domains and DNS by whole-zone replacement only, as the namecheap plugin in hollis-labs/cerberus-plugins; per-record writes stay refused because Namecheap can hide records a read-modify-write would delete."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.82
confidence_label: "Fake-backend, diff-preview and scrubber tests in the plugin; the Namecheap credential error numbers are from the documented list and wait for the operator's live check"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins:namecheap/internal/namecheapplugin/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "dns"
  - "namecheap"
  - "registrar"
  - "safety-refusal"
  - "whole-zone"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "A plugin connector since 2026-09-25, reached through the admin lane"
  - type: "relates_to"
    target: "CERB-DEC-295"
    note: "Per-record writes remain refused, now by the plugin and the contract gate"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "The third of the four to migrate"
  - type: "relates_to"
    target: "CERB-GAP-283"
    note: "The safe write path is now on every surface through connectors exec and the generated tools"
---

# Namecheap connector

> Manages Namecheap domains and DNS by whole-zone replacement only, as the namecheap plugin in hollis-labs/cerberus-plugins; per-record writes stay refused because Namecheap can hide records a read-modify-write would delete.

The Namecheap API has a trap in it, and this connector is mostly the shape of
the fence around it. `getHosts` can omit records that exist, and `setHosts`
replaces the entire zone. A per-record create or delete computed from a
`getHosts` read therefore deletes whatever the read could not see. So there is
no per-record write. The only DNS write is `set_dns_record_set`, which replaces
every host record and explicitly sets the domain's email routing, and which
requires the caller to supply the complete authoritative set.

Six operations. `list_domains`, `get_domain_status`, `list_dns_records` and
`get_dns_record_set` are `read`, `set_custom_nameservers` is `write`, and
`set_dns_record_set` is `destructive`: every record the set omits is deleted.

Since 2026-09-25 this is a plugin, not a built-in
(`docs/plans/provider-plugin-extraction.md`, H6). The id and the secret names
are unchanged: `api_user`, `api_key`, `username`, and now `client_ip`, which the
built-in read but never declared, so a plugin would otherwise never have been
handed it. The web console used to save the client IP as a field in
`infra.yaml` that nothing read, so an IP entered there never reached the
connector. It now stores it as `namecheap/client_ip`, where the plugin resolves
it.

What changed for a caller:

- **CLI.** `cerberus domain …` and `cerberus dns …` are gone, including the
  disabled `dns create` and `dns delete`, with no tombstones.
  `cerberus connectors exec namecheap <op>` runs any operation, and gives the
  record-set operations a CLI verb for the first time.
- **MCP.** The hand-written `cerberus_domain_*`, `cerberus_dns_*`,
  `cerberus_nameservers_set` and `cerberus_get/set_dns_record_set` tools are
  gone. The generated tools are `cerberus_namecheap_<op>`, served for the
  operations the operator lists under `namecheap: mcp: expose:` in
  `connector-config.yaml`.
- **Per-record writes.** The host's special case in `ExternalConnectorService`
  left with the built-in. The plugin does not declare `create_dns_record` or
  `delete_dns_record`, so the contract gate refuses them as undeclared. The
  plugin also refuses them by name, coded `invalid_args`, with the guidance to
  use `set_dns_record_set`.
- **Previews.** `set_dns_record_set`'s dry run reads the zone first and shows
  what the set would add, remove and change, including an email-routing
  change. That makes it the one preview in the lane that makes a network read,
  and it is plugin-claimed. If the read fails, the dry run fails.
- **Validation** of `email_type`, the FWD/MX conflict and each record now lives
  in the plugin, and applies to both the dry run and the real call.

## Owns

- Domain list and status
- Host-record read, and whole-zone replacement with explicit email routing
- Switching a domain to custom nameservers
- The refusal of per-record writes

## Does not own

- Per-record DNS writes, deliberately
- Registration, renewal, transfers or WHOIS privacy
- A place in the Cerberus binary

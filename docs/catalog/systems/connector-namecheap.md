---
id: "CERB-CAP-207"
class: "capability"
name: "Namecheap connector"
summary: "Manages Namecheap domains and DNS by whole-zone replacement only, having deliberately disabled per-record writes that could silently delete records."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "Refusal path verified live without a credential; the write path has unit tests but no credential to run"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/namecheap/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "dns"
  - "namecheap"
  - "registrar"
  - "safety-refusal"
  - "whole-zone"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Compiled in today, scheduled to migrate out"
  - type: "relates_to"
    target: "CERB-DEC-295"
    note: "Per-record writes are disabled on purpose, not unimplemented"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "One of the four that will migrate later"
  - type: "blocks"
    target: "CERB-GAP-283"
    note: "The only safe write path is MCP-only; the CLI exposes only the disabled commands"
---

# Namecheap connector

> Manages Namecheap domains and DNS by whole-zone replacement only, having deliberately disabled per-record writes that could silently delete records.

The Namecheap API has a trap in it, and this connector is mostly the shape of
avoiding that trap. `getHosts` can omit existing records, and `setHosts`
replaces the entire zone. Compose those naively into a per-record "create" and
you read a partial set, write it back with one addition, and silently delete
everything `getHosts` left out.

So per-record writes are **disabled**. `create_dns_record` and
`delete_dns_record` are implemented in the connector and in the service
dispatch, are absent from the declared `Definition`, and are refused at the very
top of `Execute` — before credential resolution, before the dry-run branch,
before plugin dispatch. The CLI commands still exist and are labelled
`Disabled: unsafe per-record Namecheap writes` in `--help`. Verified live on a
machine with no Namecheap credential: `cerberus dns create` returns the refusal
and its recovery, not `credential_missing`. That ordering is the whole point —
an operator learns why the operation is wrong rather than that their token is
missing.

What replaces them is `set_dns_record_set`: the caller supplies the complete
authoritative record array *and* an explicit `email_type`, both required. The
email mode has to be explicit because Namecheap's MX handling is entangled with
it — MX records under `EmailType=FWD` are refused before the write rather than
written into a broken state. The dry-run preview says plainly that all omitted
records will be deleted. That behaviour is unit-tested from several directions,
including that an empty set still carries its email mode and that an unknown
mode is refused.

The asymmetry left behind is worth naming: `get_dns_record_set` and
`set_dns_record_set` are exposed **only over MCP**
(`cerberus_get_dns_record_set`, `cerberus_set_dns_record_set`). There is no CLI
command for either. So the refusal message a CLI user gets tells them to use an
MCP tool, and the only safe Namecheap write path in the product is reachable
only by an agent.

## Owns

- Domain list and registration status
- A domain's complete host record set, read together with its email routing mode
- Whole-zone record replacement with explicit email routing
- Custom nameserver assignment
- Refusing per-record create and delete, before credential resolution

## Does not own

- Per-record DNS writes. They are disabled by decision, not missing
- Domain registration, renewal or transfer
- Authoritative DNS serving — that is Cloudflare's lane here
- Any vendor SDK — it is a hand-written XML API client

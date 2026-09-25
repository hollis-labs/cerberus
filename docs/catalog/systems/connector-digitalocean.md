---
id: "CERB-CAP-205"
class: "capability"
name: "DigitalOcean connector"
summary: "Lists, creates and manages the power state of DigitalOcean droplets as the digitalocean plugin in hollis-labs/cerberus-plugins; no longer compiled in."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.82
confidence_label: "Fake-backend, pagination, DTO and scrubber tests in the plugin; no DigitalOcean token on this machine, so live reads wait for the operator's UAT"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins:digitalocean/internal/digitaloceanplugin/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "digitalocean"
  - "droplet"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "A plugin connector since 2026-09-25, reached through the admin lane"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "The second of the four to migrate"
  - type: "relates_to"
    target: "CERB-GAP-270"
    note: "droplet start takes no dry run; the plugin refuses one, coded invalid_args"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "The plugin returns Cerberus DTOs; godo is confined to one file"
---

# DigitalOcean connector

> Lists, creates and manages the power state of DigitalOcean droplets as the digitalocean plugin in hollis-labs/cerberus-plugins; no longer compiled in.

Seven operations. `list_droplets`, `get_droplet` and `status` are `read`,
`create_droplet` is `write` with `cost: billable`, `start` and `stop` are
`lifecycle`, and `destroy` is `destructive`. Everything except the reads needs
acknowledgment. `start` needs it for the first time: before P1-1 it was not
flagged at all.

Since 2026-09-25 this is a plugin, not a built-in
(`docs/plans/provider-plugin-extraction.md`, H5). The id and the secret name are
unchanged, so `CERBERUS_DIGITALOCEAN_API_TOKEN`, a `digitalocean: api_token:`
entry in `connector-secrets.yaml` and `keychain://digitalocean/api_token`
resolve exactly as before. Removing it took `godo` and hashicorp's
`go-retryablehttp`/`go-cleanhttp` out of the binary: 23.7MB → 21.8MB stripped.

What changed for a caller:

- **CLI.** `cerberus server …` is gone, with no tombstone.
  `cerberus connectors exec digitalocean <op>` runs any operation.
- **MCP.** The hand-written `cerberus_droplet_*` tools are gone. The generated
  tools are `cerberus_digitalocean_<op>`, served for the operations the
  operator lists under `digitalocean: mcp: expose:` in `connector-config.yaml`.
- **list_droplets** reads every page, 200 at a time up to 100 pages. The
  built-in read one page of 100 and silently dropped the rest. It returns
  `{droplets, truncated}`, with `truncated: true` when the cap cut the list
  short, so a capped list is never presented as complete.
- **Previews.** `create_droplet`'s dry run shows `user_data` as
  `{bytes, sha256}`, never its content, because cloud-init routinely carries
  credentials. `start` has no preview, and a dry run of it is refused.
- **Tighter than the built-in.** `droplet_id` must be a positive whole
  number, `create_droplet` requires `name`, `start`, `stop` and `destroy`
  return `{droplet_id, action}` rather than null, and health makes no network
  call.

## Owns

- Droplet list, get, create, start, stop, destroy and status
- The godo client, confined to one file behind a DTO-returning backend

## Does not own

- Volumes, snapshots, networking, DNS, or anything else in the DigitalOcean API
- Provisioning beyond a single create call. OpenTofu is the roadmap for that
- A place in the Cerberus binary

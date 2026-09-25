---
id: "CERB-CAP-205"
class: "capability"
name: "DigitalOcean connector"
summary: "Lists, creates and manages the power state of DigitalOcean droplets through godo; since P0 create, stop and destroy are all destructive and ack-gated."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.88
confidence_label: "No token on the audit machine; flags and CLI --ack re-read on main after P0 (#48 to #54) and counted from connectors describe"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/digitalocean/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "destructive-flags"
  - "digitalocean"
  - "droplet"
  - "migrating-to-plugin"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Compiled in today, scheduled to migrate out"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "One of the four that will migrate later"
  - type: "blocks"
    target: "CERB-GAP-270"
    note: "droplet start advertised dry_run over MCP with no preview behind it; since PR #49 that dry run is refused"
  - type: "blocks"
    target: "CERB-GAP-279"
    note: "create_droplet and stop were not flagged destructive; closed in PR #49"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "Returns godo types rather than a Cerberus DTO"
---

# DigitalOcean connector

> Lists, creates and manages the power state of DigitalOcean droplets through godo; since P0 create, stop and destroy are all destructive and ack-gated.

Seven operations, the widest lifecycle surface in the lane: `list_droplets`,
`get_droplet`, `create_droplet`, `start`, `stop`, `destroy`, `status`. At audit
time it was where the lane's flag discipline was weakest, and the flags are the
only thing the enforcement points read.

PR #49 applied the house rule from `docs/plans/connector-work-packages.md`
("anything that writes, replaces, deletes or reboots gets `Destructive: true`").
`create_droplet` is destructive because it is billable and runs the supplied
cloud-init `user_data` as root. `stop` is destructive because a hard power-off
takes down whatever the droplet serves. Both keep their previews. `cerberus
server create` and `cerberus server stop` gained `--ack`, and `--dry-run` on
either still needs none. The MCP tools `cerberus_droplet_create` and
`cerberus_droplet_stop` carry `DestructiveHint: true`, and create gained an
`acknowledged` argument (CERB-GAP-279).

`start` stays non-destructive, because powering on is reversible. It has no
preview, yet the MCP tool `cerberus_droplet_start` shares a lifecycle helper
with stop and destroy and so still advertises a `dry_run` parameter described
as "Preview only". Until PR #49 the admin lane's dry-run branch fell through when
it found no preview case, and with no gate on a non-destructive operation,
`cerberus_droplet_start {droplet_id, dry_run: true}` booted the droplet. It now
returns `preview_unsupported` and runs nothing (CERB-GAP-270).

The connector otherwise follows the house pattern well: the godo client sits
behind a `Backend` interface, the credential comes from the secret provider
rather than a config field, and its tests exercise the lifecycle against a fake.
It returns `godo` types rather than Cerberus DTOs, which ADR 0003 schedules for
migration rather than immediate rewrite. There was no DigitalOcean token on the
audit machine, so `live` read `no` and no operation here was run.

## Owns

- Droplet list and get
- Droplet creation from region, size, image, SSH keys and user_data
- Droplet power on, and ack-gated power off and destroy
- Mapping droplet power state onto a Cerberus resource state
- A godo client behind a Backend interface

## Does not own

- Anything else in the DigitalOcean product surface: no volumes, no load balancers, no DNS, no Kubernetes
- Supervision of what runs on a droplet — that is ssh and docker against it afterwards
- Cost. Nothing warns that create_droplet starts billing

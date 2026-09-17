---
id: "CERB-CAP-205"
class: "capability"
name: "DigitalOcean connector"
summary: "Lists, creates and manages the power state of DigitalOcean droplets through godo, with the lane's most inconsistent destructive flags."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "No token on this machine; flag inconsistencies read directly from the definition and the dry-run switch"
last_reviewed: "2026-09-17"
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
    note: "droplet start advertises dry_run over MCP with no preview case behind it"
  - type: "blocks"
    target: "CERB-GAP-279"
    note: "Creating a billable droplet and powering a host off are not flagged destructive"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "Returns godo types rather than a Cerberus DTO"
---

# DigitalOcean connector

> Lists, creates and manages the power state of DigitalOcean droplets through godo, with the lane's most inconsistent destructive flags.

Seven operations, the widest lifecycle surface in the lane: `list_droplets`,
`get_droplet`, `create_droplet`, `start`, `stop`, `destroy`, `status`. It is also
where the lane's flag discipline is weakest, and the flags are the only thing
the enforcement points read.

`create_droplet` creates a billable machine and is flagged `destructive: false`,
so it takes no `--ack`. `stop` powers a host off and is likewise not
destructive. Only `destroy` is. The house rule in
`docs/plans/connector-work-packages.md` is explicit — "anything that writes,
replaces, deletes or reboots gets `Destructive: true`" — and two of these three
do not follow it. The reasoning is guessable (destroy is irreversible; a
power-off is not) but it is not written down, and the gate it empties is the one
an agent caller depends on.

`start` is worse than inconsistent, it is actively misleading over MCP.
`SupportsDry` is false and `dryRunPreview` has no case for it, but the MCP tool
`cerberus_droplet_start` shares a lifecycle helper with stop and destroy and so
advertises a `dry_run` parameter described as "Preview only". The admin lane's
dry-run branch falls through when it finds no preview case; for a destructive
operation it then lands on the acknowledgment gate and refuses, but `start` is
not destructive, so there is no gate. `cerberus_droplet_start {droplet_id,
dry_run: true}` boots the droplet.

The connector otherwise follows the house pattern well: the godo client sits
behind a `Backend` interface, the credential comes from the secret provider
rather than a config field, and its tests exercise the lifecycle against a fake.
It returns `godo` types rather than Cerberus DTOs, which ADR 0003 schedules for
migration rather than immediate rewrite. There is no DigitalOcean token on this
machine, so `live` reads `no` and no operation here was run.

## Owns

- Droplet list and get
- Droplet creation from region, size, image, SSH keys and user_data
- Droplet power on, power off and destroy
- Mapping droplet power state onto a Cerberus resource state
- A godo client behind a Backend interface

## Does not own

- Anything else in the DigitalOcean product surface: no volumes, no load balancers, no DNS, no Kubernetes
- Supervision of what runs on a droplet — that is ssh and docker against it afterwards
- Cost. Nothing warns that create_droplet starts billing

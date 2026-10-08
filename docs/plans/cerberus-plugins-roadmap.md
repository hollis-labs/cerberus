# Cerberus Plugins Roadmap

The provider plugins Cerberus needs: new ones, expansions of existing ones, and
conformance to new core contracts. Plugins live in `hollis-labs/cerberus-plugins`, one
directory each.

Status: agreed direction, 2026-10-04. Nothing is executed yet.
Workstream: Tether `cerberus-plugins`. Torque project: **Cerberus Plugins**.
Not to be confused with `plugin-platform`, which is the portfolio's `plugin-sdk` /
`plugin-host` work.

This work can be picked up **any time**. Some packages gate phases of
`docs/plans/compute-hosting.md` and are marked as such. Read
`docs/plans/connector-work-packages.md` (how a plugin is built and verified),
`docs/adr/0003-connector-response-dtos.md` (DTOs), and AGENTS.md "Core or plugin"
before taking a package.

## Rules every package follows

- **A plugin, never a built-in.** No vendor SDK in Cerberus's `go.mod`.
- **DTOs only** (ADR 0003). Never a vendor type, never raw vendor JSON.
- **Every write** is `Destructive` + `SupportsDry` with a real preview, behind `--ack`.
  Billable creates say so in their summary.
- **Declared secrets** come over the `Init` channel, never the environment. A missing
  credential fails per call as `credential_missing` and health still works.
- **Tools resolved per call** (`cf`, `tailscale`, `restic`). Never assume the daemon's
  `PATH` (AGENTS.md).
- **Least-privilege credentials.** Each package documents the exact token scopes it
  needs and ships a live check that confirms a read-only token is refused for writes.
- **Tests against fakes.** A live check is opt-in and confirmed by the operator.

## Packages

| # | Package | Gates | Summary |
|---|---|---|---|
| PL-1 | DigitalOcean expansion | compute-hosting P2 | Firewalls, volumes, snapshots, SSH keys, tags, reserved IPs, VPC, Spaces keys, sizes/regions |
| PL-2 | Cloudflare v1: R2 + token scopes | P0 (scopes recorded), P2 | R2 buckets, scoped R2 credentials, token verification, DNS for host records |
| PL-3 | Tailscale admin API | P2 | Auth-key mint, devices, ACL/tags. The local-CLI ops stay |
| PL-4 | GitHub forge | P2 (release assets); git-ops app; Stack Explorer | Library + host-neutral forge plugin + Cerberus adapter |
| PL-5 | Hetzner (new) | P4 | Servers, images, volumes, firewalls, networks, SSH keys, primary IPs, pricing |
| PL-6 | Backup adapters | P2 (after the core backup contract) | restic, then rsync and provider snapshots, on `cerberus.backup/*` |
| PL-7 | Cloudflare v2 on `cf` | none | New plugin on Cloudflare's `cf` CLI, coverage onboarded by product family |
| PL-8 | Future compute providers | P5–P6 | Runpod, Vast.ai. Later Vultr and Modal |

### PL-1: DigitalOcean expansion

Today: droplet `list/get/create/start/stop/destroy` + `status`. `create` supports
`user_data` and `ssh_keys`. The DTO drops VPC, volumes, backups and tags.

Add: **firewalls** (list/get/create/update/delete, assign droplets), **volumes**
(list/create/attach/detach/resize/delete), **snapshots** (droplet and volume snapshot,
list, delete), **SSH keys** (list/add/delete), **tags** and **projects** (assign),
**reserved IPs** (assign/unassign), **VPC** (list/get), **sizes/regions/images** (read,
with prices, which feeds the broker), and **Spaces access keys** (as a backup target).
Extend the droplet DTO with `vpc_uuid`, `volume_ids`, `tags` and tailnet-irrelevant
network fields through an allow-list.

Note for `stop`: **a stopped droplet still bills.** Say so in the operation summary.

### PL-2: Cloudflare v1, R2 and token scopes

Today: zones and DNS CRUD.

Add: **token verify** (`/user/tokens/verify`, which names the scopes the token holds),
**R2 buckets** (list/create/delete) and **scoped R2 S3 credentials** (create/revoke),
and DNS conveniences for host records (upsert an A record pointing at a tailnet IP).
Document the token split, **one token per use**:
- DNS-01 for Caddy: `Zone:DNS:Edit` on the operator's zone only. It lives on the host,
  outside Cerberus.
- DNS admin for Cerberus: `Zone:DNS:Edit` + `Zone:Read`.
- R2 admin: `Account:R2:Edit`. The backup credential itself is a bucket-scoped R2 key.

P0 creates these tokens by hand. This package records their scopes so P2 can reproduce
them.

### PL-3: Tailscale admin API

Today: local CLI `status/up/down/serve`, with no README.

Add an API backend (OAuth client or API key, declared secret): **auth keys** (mint a
key that is ephemeral, single-use, pre-authorized and tagged, with a short TTL; list;
revoke), **devices** (list, get, delete, set tags, authorize), **ACL policy** (get,
validate, set with preview diff), **DNS** (read). Minting an auth key is a credential
mint: it is `Destructive`, the key value is returned once, and it is registered with the
redaction scope. Write the missing README.

### PL-4: GitHub forge

Today: read-only `status`, `list_releases`, `list_workflow_runs`.

Split into three pieces (from the git-ops research), so Stack Explorer and the git-ops
app can use GitHub without depending on the Cerberus daemon:

1. **`go-github-forge` library** (in `hollis-labs/libs`): a typed client plus safe DTOs
   and their redaction tests. No Cerberus imports.
2. **Host-neutral forge plugin** on `plugin-sdk`: repos, PRs (list/get/create/merge,
   reviews, checks), issues (list/get/create/comment), notifications, Actions (runs,
   dispatch, rerun, artifacts), releases (create, upload asset, **download asset**),
   deploy keys, webhooks, GHCR packages (read). The vocabulary should leave room for
   GitLab and Gitea later (`RepoRef`, `CheckoutRef`, `ForgeState`).
3. **Cerberus adapter**: the existing `github` plugin id, which adds effect classes,
   ack, secrets, CLI and MCP conventions over (2).

P2 of compute-hosting needs release-asset download for artifact delivery. That can ship
before the rest.

### PL-5: Hetzner (new)

`hcloud-go` behind a `Backend` interface, on the DigitalOcean shape. **Servers**
(list/get/create with cloud-init and SSH keys, power on/off, rebuild, resize, delete),
**images/snapshots**, **volumes**, **firewalls**, **networks** (private), **SSH keys**,
**primary/floating IPs**, **server types/locations/pricing** (read, for the broker),
**backups** toggle. The first real workload is DR drill 2 (compute-hosting P4):
rebuild the hub on Hetzner.

### PL-6: Backup adapters

After the core backup contract lands in Cerberus (compute-hosting P2), bring `restic`
up to `cerberus.backup/{plan,run,list,restore,verify,prune}` with S3/R2, Spaces and
local repos, and add its README. Then `rsync` as a plain mirror target, and volume
snapshots from PL-1 and PL-5 as snapshot-class targets.

### PL-7: Cloudflare v2 on `cf`

Cloudflare's new `cf` CLI (beta, distributed through npm) exposes the public API as more
than 2,900 commands, mostly generated from Cloudflare's API schemas, and it is the
direction Cloudflare is heading. A v2 plugin built on it gets breadth that v1 could
never hand-write. Constraints:

- **Coverage is onboarded per product family, never as a raw passthrough.** A generic
  "run any `cf` command" op would break ADR 0003 (no DTO) and I2 (fail closed). Each
  family (DNS, R2, Tunnels, Access, certificates, Workers, WAF) is added with an effect
  class derived from the underlying HTTP verb (GET is read, everything else is a write
  with ack) and an allow-listed DTO.
- **The `cf` binary is resolved per call** (npm global paths differ by machine and the
  daemon's `PATH` is minimal). The plugin declares its Node runtime dependency.
- **Auth uses an API token** (CI mode), passed per invocation through the declared
  secret, never through `cf`'s own login state.
- **Pin the `cf` version** and run a contract test against the command schema, because
  beta commands can change.
- v1 stays until v2 reaches parity on zones, DNS and R2. Then v1 is deprecated.

### PL-8: Future compute providers

**Runpod** (pods + serverless) and **Vast.ai** (marketplace search by constraint,
on-demand and interruptible) for the GPU class. Later **Vultr** (CPU breadth) and
**Modal** (serverless). Each plugin exposes read ops for offers and pricing for the
broker, plus lifecycle ops. Scope these when compute-hosting P5 starts.

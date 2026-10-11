# Hosts

A host is a named machine Cerberus keeps a record of (ADR 0004). It is an
admin-lane target, not a supervised resource: Cerberus does not start, stop or
restart a host, and a resource may not be declared with `type: host`.

Cerberus reports facts about hosts. It does not rank them, reserve them or
choose where work goes, and no behavior differs by a host's `role` or labels.

## hosts.yaml

Declared data lives in `~/.cerberus/hosts.yaml`, outside every repo. A missing
file is an empty registry. Unknown keys are an error.

```yaml
version: 1

# Extend the known vocabularies. Without these, role must be one of
# hub, operator, worker, builder, service and provider must be homelab.
extra_roles: [gpu-farm]
extra_providers: [example-cloud]

hosts:
  - name: box-a                       # unique; lowercase letters, digits . _ -
    hostname: box-a.example.test
    tailnet_name: box-a.tailnet.example.test
    tailnet_ip: 100.64.0.10           # must be in 100.64.0.0/10 (or fd7a:115c:a1e0::/48)
    lan_ip: 192.0.2.10
    role: worker
    provider: homelab
    labels: {agents: "true", zone: a} # consumers filter on these; Cerberus does not act on them

    reach:
      ssh: box-a-ssh                  # id of an existing server/ssh resource

    declared:                         # what you say the machine is
      os: linux
      arch: amd64
      cpu: 8
      memory: 16GiB
      disk: 512GB
      gpu: none

    created_at: 2026-10-11T00:00:00Z
    owner: ops
    ttl: 72h                          # disposable hosts
    cost: {monthly: 12.5, currency: USD}

    tether_environment_id: env-0000   # opaque pointer, set by a hand-off
    backup_refs: [backup-policy-a]
```

`reach.ssh` names a `server`/`ssh` resource. The key, port, user and host-key
trust stay on that resource; a host never carries a credential.

## Probed data

Measurements are derived data kept by the daemon (`~/.cerberus/state/hosts.json`),
rebuilt by probing and safe to delete. They sit beside the declared values and
are never merged into them, so a disagreement between the two stays visible.

A probe is a read over ssh. It runs one fixed, read-only `sh -c` script on the
host (the command takes no caller input) and parses its key=value output. The
script sets its own PATH on the remote side, and the ssh connector is resolved
on every call, never from the daemon's boot PATH.

It reports: os, os version, arch, kernel; CPU count and model; memory total and
available; disk total and free for the data mount (`/data` if present, else
`/System/Volumes/Data` on macOS, else `/`); load averages; uptime; GPU count and
model (`nvidia-smi` on Linux when present, otherwise none; not determined on
macOS); and whether `docker`, `tmux`, `tailscale` and `caddy` are installed.
Linux is fully covered. macOS uses `sw_vers`, `sysctl`, `vm_stat` and `df`.
A fact a host cannot supply is simply absent.

Results are reduced to an allow-list: numbers, bools, and short tokens or model
strings that match a strict pattern. Anything else a host prints, including
anything shaped like a credential, is dropped. Errors go through the request's
redaction scope.

The `sysmon` plugin reports on the machine it runs on and takes no ssh target,
so it is not used to probe remote hosts. It would only matter for a future
on-host probe (ADR 0004, "The on-host agent").

Every host view reports `state` (`online`, `offline` or `unknown`), `probed_at`
(when the facts were measured), `checked_at` (the last attempt) and `stale`
(true when the data is older than five minutes, or there is none). A host that
does not answer is `offline`, with the last good measurement kept and its age
shown; that is a state, not an error.

## Surfaces

One service answers every surface. All four operations are reads; `probe`
changes nothing on the host. None ranks, scores, reserves or picks a host.

| Surface | list | get | probe | capacity |
|---|---|---|---|---|
| CLI | `cerberus host list` | `host get <name>` | `host probe <name>` | `host capacity` |
| Daemon socket | `GET /hosts` | `GET /hosts/{name}` | `POST /hosts/{name}/probe` | `POST /hosts/capacity` |
| Console API | `GET /api/hosts` | `GET /api/hosts/{name}` | `POST /api/hosts/{name}/probe` | `POST /api/hosts/capacity` |
| MCP | `cerberus_host_list` | `cerberus_host_get` | `cerberus_host_probe` | `cerberus_host_capacity` |

(A host named `capacity` is reached with GET; the capacity route is POST.)

The CLI uses the daemon when one answers and the in-process service when none
does, deciding before anything is sent. An error after a request was delivered
is the command's error and is never retried in process, so a probe cannot run
twice. There is no console page yet; the API routes are there for one.

CLI flags: `list` takes `--role`, `--provider`, `--state` and `--label k=v`
(repeatable). Every command takes `--json` or `-o json`.

### Capacity

`capacity` answers "which hosts satisfy these constraints, going by what was
last measured". It is a filter. The result is every host that satisfies the
query, in name order, with its probed facts, `probed_at` and `stale`.

| Constraint | CLI | Meaning |
|---|---|---|
| `min_memory_available` | `--min-memory 4GiB` | probed available memory, bytes or a size |
| `min_disk_free` | `--min-disk 50GB` | probed free disk on the data mount |
| `min_cpu` | `--min-cpu 4` | probed logical cores |
| `role`, `provider` | `--role`, `--provider` | exact match on the declared record |
| `labels` | `--label k=v` | all must match exactly |
| `require_tools` | `--tool docker` | probed tools that must be installed |
| `max_stale_age` | `--max-age 10m` | oldest probe a measured constraint is judged on (default: the stale threshold, 5m) |
| `include_offline` | `--include-offline` | judge an offline host on its last measurement instead of excluding it |
| `explain` | `--explain` | also list excluded hosts, each with its reasons |

The result has three lists. `matched` hosts satisfy every constraint. `unknown`
hosts could not be judged, each with the reason and a next step: never probed,
probed longer ago than `max_stale_age`, or the probe did not report a fact a
constraint needs (including whether a tool is installed). They are never guessed
to fit or to fail; probe them and ask again. `excluded` (only with `explain`)
lists the hosts that definitely fail, with every reason: a role or label that
does not match, a measured value below a minimum, a missing tool, or offline.

Constraints on role, provider and labels need no probe data. Only a measured
constraint (memory, disk, cpu, tools) requires fresh facts. Capacity reads
recorded facts and does not probe; a consumer that needs current facts calls
`probe` first.

There is deliberately no ranking, score, "best host", count-to-pick or
reservation in the request or the result. Placement belongs to the consumer.
Tether's provider adapter is the intended consumer (ADR 0004): it asks for the
hosts that satisfy what a workload needs, and decides for itself which to use.

### MCP results

The first content block is the result's JSON. It carries a `notes` array, so
what an agent must act on is in the text it reads, never only in `_meta`: hosts
that are stale, offline or never probed; each unknown host with its reason and
next step; and, with `explain`, each exclusion with its reasons. `get`, `probe`
and `capacity` also carry the untrusted marker for the model strings and
`reason` the remote supplied.

A refusal (an unknown host, a tool a probe does not report, a host with no
`reach.ssh`) is Cerberus-composed guidance and reaches the caller as written on
every surface.

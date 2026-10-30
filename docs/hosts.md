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

    hardware:                         # the physical machine, as recorded (never probed over it)
      vendor: LENOVO
      model: 10M2S08Y00
      version: ThinkCentre M715q
      serial: PC1ABCDE                # needs root to read, so it is recorded, not probed
      asset_tag: ""
      board: LENOVO 30FD
      bios: M11KT45A 08/09/2018
      cpu_model: AMD PRO A6-8570E R5
      cpu_socket: FT4
      memory: {type: DDR4, total: 8GiB, max: 32GiB, slots: 2, slots_used: 1, modules: ["8GiB DDR4 2400 Samsung"]}
      disks: [{name: sda, model: INTEL SSDSC2BB12, serial: BTWL0000, size: 120GB, type: ssd, bus: sata}]
      nics: [{name: enp2s0f0, mac: "6c:4b:90:59:4e:78"}]
      notes: spare 1 TB HDD bay free
      recorded_at: 2026-10-23T22:00:00Z

    tether_environment_id: env-0000   # opaque pointer, set by a hand-off
    backup_refs: [backup-policy-a]
```

`hardware` is the physical machine as the operator (or inventory tooling run
by the operator) recorded it: make, model, serial, BIOS, CPU, memory layout,
disks and network interfaces. Like `declared`, a probe never overwrites it.
Serial numbers and the memory layout need root to read, which a probe never
has, so they live only here. Strings in it that came off the machine are
marked untrusted on every surface. Disk `type` is one of ssd, hdd, nvme,
emmc, usb or other; a MAC is lowercase and colon-separated.

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
macOS); whether `docker`, `tmux`, `tailscale` and `caddy` are installed; and the
hardware identity a non-root user can read: vendor, model and version, board
and BIOS (DMI on Linux; `hw.model` on macOS), each whole disk with its size,
rotational flag, transport and model (`lsblk`, Linux), and each physical
network interface with its MAC (Linux). Firmware placeholders such as
"To Be Filled By O.E.M." are dropped. Serial numbers are never probed.
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

### Audit

Every host operation is recorded in the audit log under connector `host`,
effect `read`, with an intent and an outcome record: `list`, `get`, `capacity`
and `probe`. A record holds the caller, the operation, an HMAC digest of the
filter or constraint shape (so equal queries are recognizable without being
readable), the host name for `get` and `probe`, and on the outcome of `list`
and `capacity` a count note such as `hosts=7 matched=3 unknown=3 excluded=1`.
It never holds a host's facts, addresses, labels or probed text.

These are reads, so the repo's convention for reads applies: if the audit log
cannot be written, the read proceeds, the failure is logged, and the call is
unrecorded. Only operations that change something are refused when their
record cannot be written. A refusal by a gate (brakes, a token's scopes,
enforced policy) is recorded as a refused outcome.

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


## Shared Outpost transport

A host may select `reach.transport: outpost` and declare `reach.outpost.url`
(an HTTPS origin, or a literal loopback/Tailscale HTTP address) and
`reach.outpost.token_file` (an absolute path to a private controller token).
Pair that token locally at Outpost with scope `outpost.cerberus`. Keep
`reach.ssh` configured for independent recovery; failed or refused Outpost
mutations are never retried over SSH.

The controller token file must belong to the user running Cerberus, have mode
`0600`, and contain a nonempty token in at most 4096 bytes. A symlink at the
final path component is refused. Cerberus rereads the file for each request,
so an atomic replacement takes effect on the next call without a restart.

`cerberus host probe NAME` records the same facts through either transport.
`cerberus host plan NAME restart UNIT` previews a unit operation without
executing it. `cerberus host act NAME restart UNIT --ack` uses the shared
policy, approval, audit, and output-redaction path; an approval request must
be approved by the operator before retrying with `--approval-id`.
The fixed verbs are `units`, `logs`, `start`, `stop`, and `restart`.
`logs` accepts `--lines` up to 1000. These operate on the Outpost user's
systemd services or launchd agents. Arbitrary shell commands are not exposed.
The executor protects its own control-plane services; use SSH for upgrades.

Unit policy labels come only from the host's explicit `reach.ssh` binding to
a `server`/`ssh` resource. Without a valid binding they are unknown, which
may deny a mutation under the applied policy. A remote unit name or host
name never borrows labels from a same-named local process. Plans and audit
records retain both the host and unit, and bind the host declaration and
resolved labels to approval.

Linux mutations accept explicit `.service` names or short names without dots.
Targets, sockets, uninstantiated templates and other unit types are refused
before native dispatch. The executor resolves systemd `Id`/`Names`, preserves
case and instance identity, and checks possible dependency effects, including
conflicts, stop propagation and manager-exit actions. Incomplete metadata,
an unresolved serving unit or an inspection exceeding 128 effects refuses the
mutation. Ordinary inactive shutdown-target conflicts remain usable.

The plugin's comma-separated `protected_units` configuration adds serving
identities; whitespace is trimmed and invalid entries fail initialization.
Installed Outpost and controller identities are always protected. Linux also
discovers containing service units from its cgroup; macOS matches the plugin
or parent PID in `launchctl list`. Custom controller labels and launch layouts
that cannot be discovered must be declared in `protected_units`. Linux resolves
configured aliases too. Launchd bootstrap refuses a plist whose `Label` differs
from the requested name. These checks rely on operator-trusted supervisor
definitions: they do not sandbox a service's executable or make inspection and
dispatch atomic against concurrent operator reconfiguration. The systemd
[unit identity/dependency contract](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html)
and [supervisor properties](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html)
define the inspected metadata.

Every operation has a 30-second deadline, including launchd plist/log reads.
Files are opened nonblocking and checked on the opened descriptor; FIFOs and
other special files refuse. Plists are limited to 1 MiB and log tails to
256 KiB. Cancellation returns promptly even if a filesystem syscall stalls;
at most four filesystem workers can remain outstanding until that filesystem
recovers. Native macOS acceptance remains a separate verification step.

The HTTP routes are `POST /hosts/NAME/actions/VERB` and its `/plan` sibling,
with a `request` object containing `unit` and optional `lines` plus the usual
acknowledgment and approval fields. MCP exposes the same fixed operations.
A plan uses a separate route so older controllers cannot mistake it for an
execution. Existing Cerberus audit records cover these calls; the separate
host-action history model and SSH unit actions remain their own work items.

## Design: ceilings, storage slots and components (Hosts B, proposed)

Status: proposal for review (CW-20261030-0184). No code yet. This section
describes what Hosts B adds; the sections above stay true. Everything here
follows ADR 0004: facts only, no ranking, no reservation, and `hosts.yaml`
stays the stored source (no new database).

### Goal

Answer "what can this machine take?" as well as "what does it have?": CPU
socket and upgrade limit, memory slots and ceiling, storage bays by kind with
which are free, PCIe room, and the spare and installed parts that fill them.
Operators, Tachyon and agents read it to plan upgrades and to place spare
parts. Capacity matching is unchanged for now.

### 1. `hardware.ceilings`

Optional, recorded like the rest of `hardware` (a probe never writes it).
Every value that states a limit carries a `source`, because DMI understates
(a board reporting 32GiB may take 64GiB).

```yaml
hardware:
  ceilings:
    cpu:
      socket: {value: FT4, source: dmi}
      installed: {cores: 4, threads: 4, tdp_w: 35, source: dmi}
      max_supported: {cores: 4, threads: 4, tdp_w: 35, source: vendor_spec}
    memory:
      slots: {value: 2, source: dmi}
      max_total: {value: 32GiB, source: vendor_spec}      # DMI said 16GiB
      max_per_slot: {value: 16GiB, source: vendor_spec}
      types: {value: [DDR4], source: dmi}                 # DDR3|DDR4|DDR5|LPDDR4|LPDDR5|other
      ecc: {value: false, source: vendor_spec}
    gpu:
      pcie_slots:
        - {id: pcie0, gen: 3, lanes: 16, form: full_height, max_power_w: 75, occupant: "", source: vendor_spec}
```

- `source` is one of `dmi`, `vendor_spec`, `operator`. A value without a
  source is invalid. `operator` means someone measured or tested it and
  outranks the other two only in the operator's eyes; Cerberus does not pick
  between them, it shows the source next to the value.
- Existing `hardware.cpu_socket` and `hardware.memory.{max,slots,slots_used}`
  stay as they are (they are what 12 hosts carry today). `ceilings` does not
  replace them; where both are present they must agree on slots, or validation
  reports the disagreement. They are not merged on the view.
- Sizes use the existing `Size` type. Counts must not be negative; installed
  cores/threads/TDP cannot exceed `max_supported` when both are present.

### 2. `hardware.storage_slots[]`

One entry per physical place a drive can go.

```yaml
hardware:
  storage_slots:
    - id: m2-0                  # unique within the host
      kind: nvme_m2             # nvme_m2|sata_25|sata_35|m2_sata|ide|pcie
      form_factor: "2280"       # free text, short (2242, 2280, 2.5in, 3.5in, hhhl, ...)
      interface: pcie           # pcie|sata|ide|usb, with gen below
      gen: 3                    # interface generation (PCIe 3, SATA 3); 0 = unknown
      max_size: {value: 2TB, source: operator}
      occupant: nvme0n1         # a hardware.disks[].name, or empty = free
      notes: ""
```

- `kind` is a closed list: `nvme_m2`, `sata_25`, `sata_35`, `m2_sata`, `ide`,
  `pcie`. A new kind is a code change, so a typo cannot create a bucket.
- `occupant` is empty (free) or names an entry in `hardware.disks` (by
  `name`) or a `component` installed in this slot (by `id`, see section 3).
  Two slots cannot share an occupant; an occupant that matches nothing is a
  validation error.
- `max_size` carries a `source`; a disk larger than its slot's `max_size`
  is a validation warning, not an error, because the ceiling may be wrong
  (this is the DMI problem again).
- Derived, never stored: `storage_summary` on the view, per kind,
  `{total, used, free}` counted from `storage_slots`. A host with no
  `storage_slots` has no summary (unknown is not zero).

### 3. Components (`components`)

A component is a part, installed or spare. It is its own object so a spare
part can exist without a host, and moves between locations.

Stored in `~/.cerberus/hosts.yaml` under a new top-level `components:` list
(same file, same validation pass, no database):

```yaml
components:
  - id: ram-0007                    # unique; same name rules as host names
    kind: ram                       # ram|disk|cpu|other
    description: "16GiB DDR4 3200 SODIMM"
    size: 16GiB                     # ram and disk
    type: DDR4                      # ram: DDR3/DDR4/..., disk: ssd|hdd|nvme|emmc|usb|other
    speed: "3200"                   # free text short (MT/s, rpm, or PCIe gen)
    serial: 9876543
    location:
      host: box-a                   # an existing host name, or omit for spare
      slot: dimm1                   # a hardware slot id on that host (m2-0, ...)
      spare: false                  # true means in the spare drawer; then host/slot are absent
    notes: ""
```

- `location` is either `{spare: true}` or `{host, slot}`. A `host` must exist.
  A `slot` must match a `storage_slots[].id` for `kind: disk`, a memory slot
  position (`dimm0..dimmN-1`, N from `ceilings.memory.slots`) for `ram`, or
  `socket` for `cpu`; `other` may name any free-text slot. Two components
  cannot occupy the same host slot.
- `serial` is untrusted text like the rest; it is shown on every surface,
  because these are physical assets the operator labels.
- A component installed in a slot and the host's recorded `hardware.disks`
  can both describe the same disk. We keep both; `occupant` on the slot links
  them. The inventory collector (task D) is the writer that reconciles them.

### 4. Surfaces

Same one-service rule as today. Hosts B adds read data to existing calls and
two component reads; it adds no writes (writes are task C, 0185).

| Surface | host list/get | component list | component get |
|---|---|---|---|
| CLI | `host list/get` (gains `ceilings`, `storage_slots`, `storage_summary`) | `cerberus component list` | `component get <id>` |
| Daemon socket | `GET /hosts`, `GET /hosts/{name}` | `GET /components` | `GET /components/{id}` |
| Console API | `GET /api/hosts[...]` | `GET /api/components` | `GET /api/components/{id}` |
| MCP | `cerberus_host_list`, `cerberus_host_get` | `cerberus_component_list` | `cerberus_component_get` |

- `host list` stays a summary: it carries `storage_summary` and the memory
  headroom but not the slot detail; `get` carries everything.
- `component list` takes `--kind`, `--host`, `--spare` filters (CLI) and the
  same fields as query/arguments elsewhere.
- Audit: reads under connector `host`, effect `read`, ops `component_list`
  and `component_get`, same intent+outcome pairs and count notes.
- No ranking, no "best fit", no suggestion of which part goes where.

### 5. Validation

In `internal/hosts/validate.go`, in the same pass as `validateHardware`, with
the same text rules (length 128, no control characters) and the same
item cap (64 per list, 256 components in total). Unknown keys remain an
error. Problems report the field path (`hardware.storage_slots[1].kind`).
Cross-checks: slot ids unique per host, occupant resolves, component ids
unique, component location resolves, one component per slot, memory and CPU
ceilings consistent with the existing fields, `source` always present.

### 6. Schema

`schema/` today holds only `cerberus.yaml.schema.json`; `hosts.yaml` has no
JSON schema. Hosts B adds `schema/hosts.yaml.schema.json` covering the whole
file (existing fields plus the above), generated by hand and checked against
the Go types in a test so the two cannot drift.

### 7. Not in Hosts B

Writing any of this (C, 0185), collecting it (D, 0186), probing on a schedule
(E, 0187), capacity matching and Tachyon access (F, 0189), and backfilling the
14 hosts (Cerberus lead). Existing `hardware` data stays valid unchanged.

### Open questions for review

1. Components in `hosts.yaml` (above) versus their own `components.yaml`
   beside it. One file keeps one validation pass and one backup; two files keep
   a spare-parts edit from touching host records. Recommendation: one file.
2. `ceilings.cpu.max_supported` as a single record, or a list of supported
   CPU models. Recommendation: single record now; a list when someone needs it.
3. Do `storage_slots` need `removable` (hot-swap bays)? Left out until used.

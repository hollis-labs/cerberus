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

Status: approved design (CW-20261030-0184), implemented in the
implementation PR. This section
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
      slot_ids: {value: [dimm0, dimm1], source: dmi}      # optional; default dimm0..N-1
    pcie_slots:                                           # any card: GPU, HBA, NIC, capture
      - {id: pcie0, gen: 3, lanes: 16, form: full_height, max_power_w: 75, occupant_component: "", source: vendor_spec}
    gpu:                                                  # gpu-specific limits only; omit until needed
      power_budget_w: {value: 75, source: vendor_spec}
```

- `source` is one of `dmi`, `vendor_spec`, `operator`. A value without a
  source is invalid. `operator` means someone measured or tested it and
  outranks the other two only in the operator's eyes; Cerberus does not pick
  between them, it shows the source next to the value.
- Existing `hardware.cpu_socket` and `hardware.memory.{max,slots,slots_used}`
  stay as they are (they are what 12 hosts carry today). `ceilings` does not
  replace them; where both are present they must agree on slots, or validation
  reports the disagreement. They are not merged on the view.
- `ceilings.memory.slot_ids` lists the memory slot names a component's
  `location.slot` may use. Default `dimm0..dimmN-1`, where N is
  `ceilings.memory.slots`, or `hardware.memory.slots` when there is no
  `ceilings` block. Boards whose DMI locators are named (for example
  `ChannelA-DIMM0`) list them here.
- `pcie_slots` is for any card. `occupant_component` follows the same rules as
  on storage slots (section 2); a card is a component of kind `other`.
- Sizes use the existing `Size` type. Counts must not be negative; installed
  cores/threads/TDP cannot exceed `max_supported` when both are present.

### 2. `hardware.storage_slots[]`

One entry per physical place a drive can go.

```yaml
hardware:
  storage_slots:
    - id: m2-0                  # unique within the host
      kind: nvme_m2             # nvme_m2|sata_25|sata_35|m2_sata|msata|ide|pcie
      form_factor: "2280"       # free text, short (2242, 2280, 2.5in, 3.5in, hhhl, ...)
      interface: pcie           # pcie|sata|ide|usb, with gen below
      gen: 3                    # interface generation (PCIe 3, SATA 3); 0 = unknown
      max_size: {value: 2TB, source: operator}
      occupant_disk: nvme0n1    # a hardware.disks[].name; or occupant_component: <component id>; neither = free
      notes: ""
```

- `kind` is a closed list: `nvme_m2`, `sata_25`, `sata_35`, `m2_sata` (M.2 slot that takes SATA drives),
  `msata` (the older mSATA connector), `ide`, `pcie`. A new kind is a code change, so a typo cannot create a bucket.
- A slot is free, or has exactly one of `occupant_disk` (a
  `hardware.disks[].name`) or `occupant_component` (a component `id`, see
  section 3); both set is an error. The two namespaces can collide, so they are
  separate fields. Two slots cannot share an occupant. An occupant that
  resolves to nothing is a validation error that names which field failed
  (`hardware.storage_slots[1].occupant_disk: "sdz" is not in hardware.disks`).
- `max_size` carries a `source`; a disk larger than its slot's `max_size`
  is a validation warning, not an error, because the ceiling may be wrong
  (this is the DMI problem again).
- A component occupant must itself be located in this host and slot
  (`location.host` and `location.slot` match); the two sides must agree.
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
  A `slot` must match a `storage_slots[].id` for `kind: disk`, one of the
  memory slot ids (`ceilings.memory.slot_ids`, else `dimm0..N-1` with N from
  `ceilings.memory.slots`, else `hardware.memory.slots`) for `ram`, or `socket`
  for `cpu`; `other` may name a `pcie_slots[].id` or any free-text slot. Two components
  cannot occupy the same host slot.
- `serial` is untrusted text like the rest; it is shown on every surface,
  because these are physical assets the operator labels.
- A component installed in a slot and the host's recorded `hardware.disks`
  can both describe the same disk. We keep both; `occupant` on the slot links
  them. The inventory collector (task D) is the writer that reconciles them.

### Hardware recorded, nothing else (unknown, never zero)

Twelve hosts today have `hardware` and nothing from this section. They stay
valid unchanged, and the view says unknown rather than inventing zeros:

```yaml
hardware:
  vendor: LENOVO
  memory: {type: DDR4, total: 8GiB, max: 32GiB, slots: 2, slots_used: 1}
  disks: [{name: sda, size: 120GB, type: ssd, bus: sata}]
```

```json
{"hardware": {"memory": {"slots": 2, "slots_used": 1}, "disks": [{"name": "sda"}]},
 "ceilings": null, "storage_slots": null, "storage_summary": null,
 "notes": ["storage slots not recorded: free bays unknown"]}
```

`ceilings`, `storage_slots` and `storage_summary` are absent (`null`), never
`0` or `[]`; a list with zero entries means "recorded: none". Memory slot ids
for components fall back to `hardware.memory.slots` here.

### 4. Surfaces

Same one-service rule as today. Hosts B adds read data to existing calls and
two component reads; it adds no writes (writes are task C, 0185).

| Surface | host list/get | component list | component get |
|---|---|---|---|
| CLI | `host list/get` (gains `ceilings`, `storage_slots`, `storage_summary`) | `cerberus component list` | `component get <id>` |
| Daemon socket | `GET /hosts`, `GET /hosts/{name}` | `GET /components` | `GET /components/{id}` |
| Console API | `GET /api/hosts[...]` | `GET /api/components` | `GET /api/components/{id}` |
| MCP | `cerberus_host_list`, `cerberus_host_get` | `cerberus_component_list` | `cerberus_component_get` |

- As built, `host list` and `host get` return the same view (list already
  returned full `hardware`): `ceilings`, `storage_slots`, `storage_summary` and
  the host's `components`, with `null` for anything not recorded. The MCP list
  tool wraps the views in the usual budgeted envelope.
- `component list` takes `--kind`, `--host`, `--spare` filters (CLI) and the
  same fields as query/arguments elsewhere.
- Audit: reads under connector `host`, effect `read`, ops `component_list`
  and `component_get`, same intent+outcome pairs and count notes. The `list`
  note for hosts gains `components=N` (components located on the listed hosts);
  `component_list` carries `components=N spare=M`.
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
the Go types in a drift test. The schema file and the drift test land in the
implementation PR, not in this docs-only PR.

### 7. Not in Hosts B

Writing any of this (C, 0185), collecting it (D, 0186), probing on a schedule
(E, 0187), capacity matching and Tachyon access (F, 0189), and backfilling the
14 hosts (Cerberus lead). Existing `hardware` data stays valid unchanged.

### Open questions for review

Settled in review (Cerberus lead, 2026-10-30): components stay in
`hosts.yaml` (one file, one validation pass); `cpu.max_supported` is a single
record; no `removable` flag until needed.

## Writes and history (Hosts C)

`hosts.yaml` stays the stored source. Hosts C adds validated writes to it, over
every surface, and an append-only history of what changed.

| Operation | CLI | Daemon socket / console API | MCP |
|---|---|---|---|
| change a host (merge patch; `create` adds it) | `host set NAME` | `PATCH /hosts/{name}` · `/api/hosts/{name}` | `cerberus_host_update` |
| remove a host | `host remove NAME` | `DELETE /hosts/{name}` | `cerberus_host_remove` |
| add a part | `component create` | `POST /components` | `cerberus_component_create` |
| change a part | `component update ID` | `PATCH /components/{id}` | `cerberus_component_update` |
| move a part (slot, host or spare) | `component move ID` | `POST /components/{id}/move` | `cerberus_component_move` |
| remove a part | `component remove ID` | `DELETE /components/{id}` | `cerberus_component_remove` |
| revision (the write token) | printed by `--revision` default | `GET /hosts/_revision` | `cerberus_host_revision` |
| history | `host history NAME`, `component history ID` | `GET /hosts/{name}/history`, `GET /components/{id}/history` | `cerberus_host_history`, `cerberus_component_history` |

A host change is a JSON merge patch (RFC 7386) on the host record: objects
merge, `null` deletes a field, a list replaces. `name` and a component `id`
never change; a component's location changes only with a move. Example, the
8 GiB to 16 GiB upgrade on worker-1, as one change that records both sides:

```
cerberus host set worker-1 --ack --patch \
  '{"hardware":{"memory":{"total":"16GiB","slots_used":2,"modules":["8GiB DDR4 2400 Samsung","8GiB DDR4 2400 Crucial"]}}}'
```

### Safety

A bad `hosts.yaml` makes every host call fail, so a write is built to never
leave one:

1. **Whole-file validation.** The change is applied to the file as it is under
   the lock, the complete result goes through the same validator every read
   uses, and the bytes that would be written are parsed again. Any failure
   returns the error and writes nothing: the file is byte-identical, with no
   backup, temp file or history entry left behind.
2. **Atomic write.** Temp file in the same directory, `fsync`, `rename`, `fsync`
   of the directory; mode is kept (new files are `0600`).
3. **Backup.** The previous bytes are kept as `hosts.yaml.bak-<UTC timestamp>`;
   the newest 20 are kept.
4. **One writer at a time.** An exclusive lock (`hosts.yaml.lock`) serializes
   writers.
5. **Revision token.** Every write carries the revision (a digest of the file)
   the caller read: `revision` in the body, `If-Match`, or `--revision` (the CLI
   defaults to the current one, read just before). A stale revision is refused
   with 409 and writes nothing. The patch itself applies to the file as it is
   under the lock, so a write never overwrites fields it did not name.
6. **Referential checks.** A host with components located on it cannot be
   removed; a move fills the target slot and frees the old one, and a taken
   slot is refused. No resource refers to a host by name, so components are
   the only references.
7. **Format is kept.** A file written as JSON (the fleet inventory script reads
   and writes it with a JSON library) stays JSON; a YAML file stays YAML.
   Comments in a YAML file are not preserved; the backup has them.
8. **No credentials.** A patch or component containing a string the redaction
   rules would mask is refused. Unknown keys are refused by the same strict
   decoder as a read.

### Audit and gates

Every write is an `effect: write` operation under connector `host`
(`host_update`, `host_remove`, `component_create`, `component_update`,
`component_move`, `component_remove`) with an intent and an outcome record. As
for every write, if the intent record cannot be written the operation is
refused and nothing runs (reads still proceed unrecorded). A write goes through
the same gates as any other write: a verified token needs the write scope (token
scopes follow the operation's effect, so there is no separate `host.write`
scope), brakes, policy, and an approval where policy asks for one. A write needs
the caller's acknowledgment (`--ack`, `acknowledged`) like every non-read.

### History

`hosts.history.jsonl` sits beside `hosts.yaml` (`0600`, append-only). Each entry
records the time, the caller (surface, client, subject), the operation, the
record, the hosts it touches, the audit operation id, the file's revision before
and after, and only the fields that changed, before and after, redacted. A
component move lists both the host it left and the host it joined, so a host's
history shows parts moved on and off. At 1 MiB the file rotates to
`hosts.history.jsonl.1`, `.2`, `.3`; older entries are dropped. If the history
cannot be appended after the file was written, the write stands and the result
carries a warning. Reads: `GET /hosts/{name}/history`, `GET
/components/{id}/history` (`?limit=`), `host history`, `component history`,
`cerberus_host_history`, `cerberus_component_history`.

## Inventory (Hosts D)

`cerberus host inventory NAME [--dry-run] --ack` (`POST /hosts/{name}/inventory`,
`cerberus_host_inventory`) collects a host's physical facts and records them
through the Hosts C write path, replacing `sysadmin/fleet/fleet-inventory.py`.

- **Collection.** One fixed, read-only `sh` script over the host's `reach.ssh`
  (it takes no input): DMI identity (vendor, model, version, serial, board, BIOS,
  asset tag), CPU model and socket, memory layout (`dmidecode -t 16,17`), whole
  disks with serials (`lsblk`) and NICs. Root-only facts run under `sudo -n`;
  without passwordless sudo they are skipped, the host is reported as partial,
  and the recorded serials, socket and memory layout are kept. The remote run is
  audited as `ssh exec` (read-sensitive). Outpost has no privileged inventory
  operation yet, so a host with only an Outpost reach is refused with guidance.
- **Merge.** Collected facts replace the collected fields of `hardware`.
  Operator data is never touched: `notes`, `storage_slots`, ceilings whose
  source is `vendor_spec` or `operator`, and every component. Ceilings whose
  source is `dmi` (memory slots, maximum, types, ECC; CPU socket and installed
  cores) are filled or refreshed from the collection.
- **Change event.** `recorded_at` moves, and a `host_inventory` history entry
  with the changed fields is written, only when a fact differs; an identical run
  writes nothing. A collection that would break the file (for example a disk
  renamed under a slot's `occupant_disk`) is refused by validation with the
  slot named; nothing is silently cleared.
- **Write path.** Same as Hosts C: whole-file validation, backup, audit, gates,
  acknowledgment. `revision` is optional here (the run gathers its own data): the
  current one is used and the write retried if another writer got in first.
  `--dry-run` shows the change and writes nothing.

## Design: monitoring (Hosts E, proposed)

Status: proposal for review (CW-20261030-0187). No code yet. Goal: every host
has a recent, trustworthy status instead of a point-in-time probe, with retained
samples for trends and events for subscribers. Capacity stays a filter (ADR
0004): nothing here ranks, scores or picks a host.

### 1. Scheduling: a host monitor in the daemon

A `HostMonitor` in the daemon, started and stopped like `ResourceMonitor`. It is
**not** a schedule-service job. The scheduler core ships inactive (no engine, no
executor, no per-fire authority), has no host-probe target, caps a fire at 30 s
and models user jobs with app grants; making it probe hosts would mean enabling
the engine and a new target kind, which is a larger and separately gated
change. The monitor calls the same `probe` the CLI does, so there is one probe
implementation.

- **Intervals.** Default 60 s for hosts whose transport is `outpost`, 5 min for
  `ssh`, `none` is never probed. Daemon settings give the defaults and a floor
  (15 s); a host may override with an optional validated `monitor:` block in
  `hosts.yaml` (`enabled`, `interval`), changed through the Hosts C write path.
- **No hammering.** Each host has its own timer with ±10 % jitter and a staggered
  start (the first probe of host *i* is delayed by a random slice of its interval,
  so a daemon restart does not probe all hosts at once). A global concurrency cap
  (default 4) and a per-transport cap (default 2 for ssh). A host whose previous
  probe is still running is skipped. After a failure the host backs off:
  interval × 2^n, capped at 15 min, reset on the first success. A probe that
  takes longer than its timeout counts as a failure.
- **Rollout.** Off by default (`host_monitor.enabled: false`); the operator turns
  it on after the deploy. `cerberus host monitor` shows what is scheduled, when
  each host was last attempted and when it is next due.
- **Audit volume.** 14 hosts at 60 s would be ~20 000 intent/outcome pairs a day.
  Monitor probes run as the automation principal and are recorded as one record
  per **state change**, one per failed probe run of a streak (not per retry), and
  an hourly summary (counts per host); routine successful samples are not audited
  one by one. Operator-initiated probes are audited as today.

### 2. Storage: Cerberus's SQLite

The daemon's application database (already opened for scheduling, WAL,
`modernc.org/sqlite`) gets a migration with four tables. No new file and no new
database; `hosts.yaml` stays the declared source and `state/hosts.json` stays the
last-probe cache for `list/get/capacity`.

| Table | Content |
|---|---|
| `host_samples` | one row per successful probe: `host, ts, load1, load5, load15, cpu, mem_total, mem_available, disk_total, disk_free, uptime_s, probe_ms`; primary key `(host, ts)`, `WITHOUT ROWID` |
| `host_sample_rollups` | `host, grain (5m/1h/1d), bucket_ts, n`, and per measure min/avg/max; primary key `(host, grain, bucket_ts)` |
| `host_status` | one row per host: `status, reasons (JSON), since, last_ok, last_attempt, consecutive_failures, next_due` |
| `host_events` | append-only: `id (autoincrement), ts, host, kind, from_status, to_status, reasons (JSON)` |

- **Retention classes** (daemon settings; defaults): raw samples 7 d; 5-minute
  rollups 30 d; hourly rollups 400 d; daily rollups kept. Events 90 d. At 60 s a
  host writes ~10 000 raw rows a week, 14 hosts ~140 000: small.
- **Rollups and janitor.** An hourly janitor builds rollups from a per-grain
  watermark (idempotent), then deletes raw rows only after the rollup that
  counts them is committed, in one transaction per batch; every removal is
  counted in the janitor's log line. Same rule as the data-paths direction (D7):
  no row is removed unless it is counted into a committed rollup.
- **Why not the reporting Postgres.** Cerberus already owns this SQLite and the
  planned reporting database is not built. `host_events` is cursor-readable (by
  `id`), so a later sink can copy events and rollups to reporting without
  changing the monitor.
- **Tether session counts.** Cerberus has no dependency on Tether and holds no
  Tether credential. Proposed: a generic `host_metrics (host, ts, name, value)`
  table plus a write endpoint (`POST /hosts/{name}/metrics`, scope write, one
  value per call or a small batch) that Tether calls on its own cadence with the
  count of sessions per worker. The status/samples reads include any external
  metric under its name. v1 ships the table and endpoint; Tether pushing is its
  own change. See the open question below.

### 3. Status and health

`status` is derived by the monitor after each attempt, stored in `host_status`,
and attached to `HostView` (additive field). Values:

- `online`: the last probe succeeded and is newer than 3 intervals.
- `degraded`: online, and at least one of the documented thresholds is crossed
  (below), or the last probe was a partial result.
- `offline`: 3 consecutive failed probes, or no success within 3 intervals.
- `unknown`: never probed, or the host is not monitored.

Every value carries `reasons` (code and plain-words detail), `since`, and the age
of the last good measurement; stale reasons are listed as `stale` with the age,
never silently hidden. Thresholds are facts the operator can read and change, not
scores: `disk_low` (free < 10 % or < 5 GiB), `memory_low` (available < 10 %),
`load_high` (load1 > 2 × cpu for 3 samples), `probe_slow` (probe_ms > half the
timeout for 3 samples). **Flap damping:** a change to or from `degraded` needs 2
consecutive consistent results; `offline` needs the 3 failures above; recovery to
`online` needs 1 success. A host labelled `intermittent` (a laptop) is still
reported as it is, with `expected: true` on its offline events so a subscriber can
mute it.

Reads, on all four surfaces: `GET /hosts/{name}/status`,
`GET /hosts/{name}/samples?since=&until=&grain=raw|5m|1h|1d&limit=`,
`cerberus host status [NAME]`, `cerberus host samples NAME`,
`cerberus_host_status`, `cerberus_host_samples`. Samples return the grain asked
for, in time order, with `n` per bucket for rollups.

### 4. Events

A change of `status` appends a `host_events` row (`status_changed` with from, to
and reasons) and wakes subscribers. Events carry status and reasons only: no
serials, addresses or probed text.

- **SSE.** `GET /api/hosts/events` on the console API (and `GET /hosts/events` on
  the daemon socket): `text/event-stream`, one event per `host_events` row with
  `id:` set to the row id; the client resumes with `Last-Event-ID` (events older
  than retention are reported as a `gap` event); heartbeat comment every 20 s;
  filters `?host=` and `?kind=`. A subscriber that cannot keep up is disconnected
  and resumes by id; the monitor never blocks on a subscriber.
- **MCP** has no streaming, so `cerberus_host_events(since_id, limit)` polls the
  same rows.
- **Auth.** Same as the other console routes today (session). The service
  credential and narrower scope for Tachyon belong to Hosts F.

### 5. Hosts E in pieces

1. **E1** migration, monitor (timers, jitter, backoff, caps), samples, status on
   `HostView`, `host monitor/status/samples`, MCP and API reads, config + schema.
2. **E2** rollups and the retention janitor.
3. **E3** events, SSE, `cerberus_host_events`.
4. **E4** external metrics table and endpoint.

Each is one PR with tests: timer/jitter/backoff with a fake clock, concurrency cap
with a blocking fake probe, no probe pile-up after a restart, status transitions
including flap damping, janitor idempotence and "never delete before the rollup
commits", SSE resume and gap, migration up on an existing database, and the
no-ranking rule (status and samples contain no score or order beyond name/time).

### Open questions for review

1. Monitor in the daemon (above) versus a schedule-service job with a new
   `host_probe` target: recommendation, the monitor.
2. Default off until the operator enables it: recommendation, yes.
3. Source of Tether session counts: Tether pushes via the metrics endpoint
   (recommended) versus Cerberus pulling from Tether's API with a credential
   Cerberus would have to hold.
4. Audit policy for monitor probes (state changes, failure streaks and an hourly
   summary only): confirm, or ask for every probe.
5. Retention numbers and status thresholds above are defaults to confirm.

### Hosts E1 as built

E1 implements section 1 to 3 of the design above except rollups (E2), events and
SSE (E3). Decisions from the review: the monitor lives in the daemon, is off by
default, monitor probes are audited as designed, thresholds are daemon settings
whose effective values `host status` shows, and the external metrics endpoint
(E4) is not built (Tachyon joins Tether session counts itself).

- **Enable it** in the daemon config (`~/.cerberus/config.yaml`):
  `host_monitor: {enabled: true}`, then restart the daemon. Every other setting is
  optional (`outpost_interval`, `ssh_interval`, `min_interval`, `jitter_percent`,
  `max_backoff`, `concurrency`, `ssh_concurrency`, `probe_timeout`, `thresholds:`);
  see `schema/cerberus.yaml.schema.json`. A host can override with
  `monitor: {enabled: false}` or `monitor: {interval: 30s}` in `hosts.yaml`.
- **Reads**: `cerberus host status [NAME]`, `host samples NAME [--since 6h]`,
  `host monitor`; `GET /hosts/_status`, `/hosts/{name}/status`,
  `/hosts/{name}/samples`, `/hosts/_monitor` (socket and `/api/hosts/...`);
  `cerberus_host_status`, `cerberus_host_samples`, `cerberus_host_monitor`. The
  host view gains `status` (absent when no monitor runs: nothing known, not
  healthy). Only raw samples exist until E2; a rollup grain is refused.
- **Storage**: migration 006 adds `host_samples` and `host_status` to the
  application SQLite. Raw samples are not pruned until the E2 janitor can roll
  them up first.
- **Probing**: monitor probes use the host service directly, never the daemon's
  own socket, so probing the host the daemon runs on (over Outpost or ssh) cannot
  recurse. They do not touch the OS keyring, and the monitor is built after the
  socket-independent startup work: a monitor or database that fails to come up is
  a logged warning, not a reason the daemon fails.
- **Probe concurrency**: the host service no longer holds its mutex across the
  remote measurement (it did, which serialized every probe); only the stored-state
  update is serialized.

## Design: capacity on hardware and load, service credential, published schema (Hosts F, proposed)

Status: proposal for review (CW-20261030-0189). No code yet. F depends on C
(writes, history) and E (samples, status, events). Capacity stays a filter (ADR
0004): none of this ranks, scores, reserves or picks a host. Placement belongs to
the consumer; the main consumer is Tachyon.

### 1. Capacity constraints on hardware and recent load

`POST /hosts/capacity` (socket), `POST /api/hosts/capacity` and
`cerberus_host_capacity` / `cerberus host capacity` gain constraints. Every one
is a yes/no test on a recorded or measured fact; a constraint whose fact is
missing puts the host under `unknown` with the reason and a next step, never
guessed to fit or fail (the rule the existing constraints follow).

| Constraint | Meaning | Fact it needs |
|---|---|---|
| `min_memory_total` | installed memory at least this | probed `memory_total`, else `hardware.memory.total` |
| `min_memory_upgrade` | room to add at least this much memory | `ceilings.memory.max_total` minus installed total |
| `free_memory_slots` | at least N empty memory slots | `hardware.memory.slots - slots_used` |
| `free_storage_slots` | `[{kind: nvme_m2, min: 1}]`, free slots by kind | `storage_slots` (not recorded: unknown) |
| `disk_types` | at least one disk of these types (`nvme`, `ssd`, `hdd`) | `hardware.disks[].type` |
| `min_disk_size` | a disk at least this large | `hardware.disks[].size` |
| `status_in` | monitor status is one of `online`, `degraded` | host status (E) |
| `recent` | headroom held over a window, below | samples (E) |

`recent` is `{window: 15m, max_load_per_core: 1.0, min_memory_available: 2GiB,
min_disk_free: 20GiB}`: the host passes when its **worst** sample in the window
(highest load per core, lowest memory available, lowest free disk) still meets
every limit given, so "has headroom for this workload" means it had it throughout,
not on average. A window with fewer samples than half of what its interval would
give (or none) puts the host under `unknown` with `"3 of 15 expected samples"`.
Windows up to the raw retention (7 d) work from E1; longer ones need the E2
rollups and are refused until then. Defaults: `window` 15 m.

The result keeps its three lists (`matched`, `unknown`, `excluded`) in name order.
Each matched host carries the facts it was judged on (the worst-case values in the
window and their sample count), so the consumer can decide between matches
itself. A test asserts the order stays by name whatever the facts are, and that the
request and result carry no weight, score, preference or count-to-pick.

### 2. Service credential and narrower scopes for Tachyon

Today a token's scopes follow the operation's effect (`cerberus:read`,
`cerberus:read_sensitive`, `cerberus:operate`) and the console API answers only a
signed-in browser session, so a service such as Tachyon has no non-interactive
way into `/api/hosts`. The Tether gateway already reaches the host tools over MCP
with its own principal; this adds the HTTP path.

- **Reuse the existing token system.** No new credential store. The tokens issued
  and verified for mcp-http (`cerberus mcp-http token issue --client NAME --scope
  ...`) are accepted as `Authorization: Bearer` on `/api/hosts*`, `/api/components*`
  and the schema route only; every other console route stays session-only. A bearer
  call has no ambient credential, so it is exempt from the browser CSRF guard but
  not from anything else: the token's scopes, brakes, policy, audit (the token id
  and subject are in every record) and acknowledgment still apply.
- **Domain scopes.** Two new scopes, narrower than the effect scopes:
  `cerberus:hosts.read` covers the read operations of connector `host` (hosts,
  components, status, samples, events, capacity, history, schema) and nothing
  else; `cerberus:hosts.write` adds `host_update`, `component_create`,
  `component_update`, `component_move`, `component_remove` and `host_inventory`.
  `host_remove` and unit actions (start/stop/restart over Outpost) stay under
  `cerberus:operate`. `cerberus:operate` still covers everything. A token with only
  `hosts.write` therefore cannot touch a resource, a pipeline or a secret.
- **How the check works.** `oauth.Allows` today maps an effect to a rank. It gains
  an operation-aware form, `AllowsOp(scopes, connector, operation, effect)`: true
  if the effect scope allows it, or the connector is `host` and the operation is in
  the set the domain scope names. The refusal text names the scope to ask for, as
  it does today.
- **Issuing.** Issuing a credential is the operator's decision. This change adds
  the scopes and the bearer acceptance; it issues nothing. The note in the PR gives
  the command for the operator to run for Tachyon.

### 3. Published schema

A machine-readable description of the host API so Tachyon (and any other client)
binds to a contract, not to Go struct names.

- **OpenAPI 3.1** at `schema/hosts.openapi.json`, served at
  `GET /api/hosts/_openapi` and `GET /hosts/_openapi`, with component schemas for
  `Host`, `HostView`, `Component`, `StorageSlot`, `Ceilings`, `WriteResult`,
  `Change`, `HostStatus`, `Sample`, `MonitorSnapshot`, `CapacityQuery`,
  `CapacityResult` and the event objects, and each route's method, path, scope,
  request and response. `schema/hosts.yaml.schema.json` stays the schema of the file.
- **One table, not two descriptions.** The routes become a table (method, path,
  summary, required scope, request type, response type) that the document is
  generated from and that a test walks against the real handlers, so a route cannot
  exist undocumented or be documented and missing. The component schemas are
  generated from the Go DTOs by reflection (as the drift test already reads
  `hosts.yaml.schema.json`), and the committed file is checked against a fresh
  generation in CI.
- **Untrusted text** fields carry `x-cerberus-untrusted: true`, so a consumer that
  shows model- or host-supplied strings knows which ones.
- **Naming.** `hosts` is canonical (decided); no `machines` alias.

### 4. Event stream for Tachyon

The SSE endpoint is E3. F adds to it the bearer path of section 2 (a token with
`cerberus:hosts.read` may subscribe, resuming with `Last-Event-ID`) and puts the
stream and its event objects in the OpenAPI document. A subscriber's token is
checked when it connects and again at each heartbeat, so revoking it ends the
stream within one heartbeat.

### 5. Hosts F in pieces

1. **F1** capacity constraints (section 1), docs, MCP/CLI/API, tests. Needs E1.
2. **F2** domain scopes and bearer acceptance on the host routes (section 2).
3. **F3** the route table, OpenAPI generation, `_openapi` routes and the CI drift
   check (section 3).
4. **F4** bearer on SSE and the event objects in the document. Needs E3.

### Open questions for review

1. Scope names and split: `cerberus:hosts.read` / `cerberus:hosts.write`, with
   `host_remove` and unit actions staying under operate. Confirm or change.
2. Does Tachyon want the HTTP console API (bearer) or is the MCP route through the
   Tether gateway enough for it? This decides whether F2 and F4 are needed now.
   Needs the Tachyon Architect's answer; the lead coordinates.
3. `recent` judged on the worst sample in the window (recommended) versus average
   or a percentile.
4. Insufficient samples as `unknown` (recommended) rather than excluded.
5. Whether `status_in` should default to excluding `offline` hosts when set at all,
   or stay an explicit filter (recommended: explicit).

### Hosts E2 as built

Rollups and retention, as designed. Migration 007 adds `host_sample_rollups` and
`host_rollup_state`.

- **Rollups.** Each bucket of 5 minutes, 1 hour and 1 day holds the sample count
  and the sum, minimum and maximum of load1, memory available, free disk and probe
  time (plus the maximum of uptime, cores and totals), so a coarser grain combines
  the finer one exactly: the counts and sums of every grain add up to the raw
  samples they cover. A measure a host never reported (no load) is absent, not
  zero. 1 h is built from 5 m and 1 d from 1 h, only from buckets that are
  complete; the end of the last committed bucket is the grain's watermark.
- **Retention.** Hourly, starting a minute after the daemon starts and whether or
  not probing is on: build the missing rollups, then delete raw samples older than
  `sample_retention` (default 7 d), 5-minute rollups older than
  `rollup_5m_retention` (30 d) and hourly rollups older than `rollup_1h_retention`
  (400 d), each only **below the watermark of the tier that counts it**, in its
  own transaction. Daily rollups are kept. A failure leaves the finer data in
  place; a second run changes nothing. Each run that did something is logged
  with its counts and recorded as a `monitor_event` ("compacted").
- **Reads.** `GET /hosts/{name}/samples?grain=5m|1h|1d` (and `/api/hosts/...`),
  `cerberus host samples NAME --grain 1h`, `cerberus_host_samples` with `grain`.
  Raw (the default) returns samples; a rollup grain returns buckets with `n` and
  `min/avg/max`.

### Hosts E3 as built

Status events and the stream, as designed. Migration 008 adds `host_events`.

- **Events.** Every change of a host's status appends a `status_changed` event
  (`id`, `ts`, `host`, `from`, `to`, reason `codes`). Events carry codes only: no
  serials, addresses or probed text, so they are safe to hand to any subscriber. A
  change on a host labelled `intermittent: "true"` carries `expected: true`, so a
  subscriber can mute a laptop's flaps. Ids only grow and are never reused. Events
  are kept `event_retention` (default 90 d) and removed by the hourly janitor.
- **Poll.** `GET /hosts/_events?after_id=N&host=&limit=` (and `/api/hosts/_events`)
  returns `{events, last_id, gap?}` oldest first; `cerberus host events [--after-id N]
  [--follow]`; MCP `cerberus_host_events`. Resume from `last_id`.
- **Stream (SSE).** The same route with `Accept: text/event-stream` (or
  `?stream=1`) streams one event per `host_events` row with `id:` set to the row id.
  Resume with `Last-Event-ID`. If events after that id were pruned, a `gap` event
  `{after_id, oldest_id}` comes first and then everything still kept. A comment
  heartbeat every 20 s. The stream audits once when it opens, not per batch. At
  most 16 streams are open at once (a 17th is refused with guidance). The monitor
  only signals a one-slot channel per subscriber, so a slow or stuck subscriber can
  never block probing; it reads the log by id when it next runs.
- **Auth.** Same as the other console routes today (a signed-in session; the
  socket for local clients). Bearer access for services is Hosts F.

### Hosts F1 as built

Capacity gains the hardware, status and recent-headroom constraints of the Hosts F
design. Capacity is still a filter: a host is matched, unknown or excluded, in name
order, with no weight, score or count-to-pick.

| Constraint | CLI flag | Needs |
|---|---|---|
| `min_memory_total` | `--min-memory-total 16GiB` | probed total (fresh), else `hardware.memory.total` |
| `min_memory_upgrade` | `--min-memory-upgrade 32GiB` | `ceilings.memory.max_total` and the installed total |
| `free_memory_slots` | `--free-memory-slots 2` | `hardware.memory.slots` / `slots_used` |
| `free_storage_slots` | `--free-slot nvme_m2=1` | `storage_slots` recorded |
| `disk_types` | `--disk-type nvme` | `hardware.disks` recorded |
| `min_disk_size` | `--min-disk-size 500GB` | `hardware.disks` recorded |
| `status_in` | `--status online --status degraded` | a running monitor |
| `recent` | `--recent-max-load 1.0 --recent-min-memory 4GiB --recent-min-disk 50GB [--recent-window 30m]` | samples |

A host whose fact is not recorded is listed under `unknown` with what is missing and
a next step, never guessed to fit or to fail. `recent` passes a host only if its
**worst** sample in the window (highest load per core, lowest memory available,
lowest free disk) meets every limit given; fewer than half the expected samples
(window divided by the host's probe interval) is `unknown` ("1 of 3 expected
samples"). The window defaults to 15 m and is at most 7 d. The result's `judged`
map gives, per matched host, the worst values and the sample count the host was
judged on, so a consumer can choose between matches itself; it is keyed by host
name and orders nothing. It also says where the facts came from: `probe_interval`
and `window` for `recent`, and `status_facts` (status, since, last good
measurement, probe interval) for `status_in`, so a consumer can show freshness.

### Hosts F3 as built

The host API is published as OpenAPI 3.1 in `schema/hosts.openapi.json`, and served
by every daemon and console at `GET /hosts/_openapi` and `GET /api/hosts/_openapi`
(under `/api/hosts`, so a bearer token reaches it).

- **One source.** The routes are a table (`cerbapi.HostRoutes`: method, path, operation,
  summary, request and response types, query parameters). The document is generated
  from that table and, by reflection, from the Go types the routes send and return, so
  the schemas are the real wire shapes: `HostView` has `ceilings`, `storage_slots` and
  `storage_summary` as nullable (absent is not zero), `Event` carries codes only, a size
  is an integer or a string such as `16GiB`.
- **Scopes are the enforced ones.** Each operation's `x-cerberus-scope` comes from
  `oauth.RequiredScopeOp`, the same function the daemon enforces with, so the
  documented scope cannot differ from the real one.
- **Untrusted text.** Strings that came from a host or an operator (`cerb:"untrusted"`
  in the Go types) carry `x-cerberus-untrusted: true`; a consumer that shows them must
  not treat them as instructions.
- **No drift, no network.** The committed file is compared with a fresh in-process
  generation in the normal test run (`UPDATE_OPENAPI=1 go test ./internal/cerbapi -run
  TestOpenAPIMatchesTheCommittedFile` rewrites it). Other tests check that every
  documented route reaches a real handler, that every host operation is documented or
  deliberately not (the Outpost unit actions and the monitor audit record are not
  published), that the request bodies match what the handlers accept, that every `$ref`
  resolves, and that the output is deterministic.

# ADR 0005: Host Actions, Asset Records, and the Outpost Transport

## Status

Proposed, 2026-10-16. Direction agreed with the operator in conversation; the
shape below is the basis for implementation, nothing is built. Items marked
**Assumption** rest on designs this repo does not own (the Outpost plugin
contract, Tachyon) and must be checked against them before work starts.

Extends ADR 0004. Answers the on-host agent question left open there
(`CW-20261011-0065`). Workstream: Torque epic **Fleet & Host Management**
(`EP-20261011-0001`).

## Date

2026-10-16

## Context

ADR 0004 gave Cerberus the Host object, read-only probing and a capacity query.
It deliberately left two things open: whether to install anything on a managed
host, and how a host is acted on (restart, stop). The operator now wants a
control surface (the Tachyon app) that can view hosts, monitor them, and
restart or stop what runs on them, plus the asset-management record that goes
with a machine (serial number, purchase and warranty data, date added).

Since ADR 0004, Tether settled on one worker per machine. That worker is the
**Outpost**: a plugin host with common app scaffolding (pairing, device tokens,
a transport to the hub), on which Tether is one plugin among several. A Cerberus
plugin on the Outpost is therefore a peer of Tether's, not a dependency on it.
The ADR 0004 seam (Cerberus owns hosts, Tether consumes them) is unaffected,
because Cerberus would reach hosts through the neutral Outpost rather than
through Tether.

## Decision

### 1. Reach is a transport, behind the same Host object

`Reach` gains a transport:

| Transport | Meaning |
|---|---|
| `ssh` | Today's behavior: a reference to a `server`/`ssh` resource. Always available as a fallback. |
| `outpost` | The host runs an Outpost paired with this Cerberus; operations travel over its Cerberus plugin channel. |
| `none` | Recorded for inventory only (an asset in a drawer, a machine not yet onboarded). |

A transport is an interface behind the existing probe and action code. `ssh`
is implemented first; `outpost` follows when its contract exists. A host with
an Outpost keeps its ssh reach, because a dead Outpost must remain diagnosable.

The Outpost-side plugin is a **thin executor**: it runs a fixed, allow-listed
set of verbs and holds no policy. Cerberus decides, gates, approves and audits;
the plugin never grants itself authority. This is the same stance as the
acknowledgment gate and `docs/plans/live-systems-security-target.md`: policy
lives in one place, and a second implementation of it on every machine would be
a second thing to get wrong.

**Assumption:** the Outpost plugin contract supports a request/response channel
authenticated by its pairing tokens and a per-plugin scope. Verify against the
Outpost design before implementation.

### 2. Asset record

Typical asset-management data lives on the Host, in its own group, separate
from the declared and probed specs (it is a record of ownership, not of
capability, and a probe never touches it):

| Field | Notes |
|---|---|
| `serial_number`, `asset_tag` | Identity of the physical unit |
| `manufacturer`, `model` | |
| `acquired_at`, `added_at` | Date acquired; date added to Cerberus (set once, never rewritten) |
| `purchase` | vendor, price, currency, order reference |
| `warranty_until`, `support_contact` | |
| `owner`, `custodian` | Who owns it; who looks after it |
| `location` | Free text (room, rack, "work bag") |
| `disposed_at`, `disposal_note` | Lifecycle end; a disposed host stays on record |
| `notes` | Free text |

Declared data, in `hosts.yaml`, operator-owned and outside every repo. Serial
numbers and purchase data are operator-sensitive: they go through the redaction
scope like any other response, and none is a credential. A probe may *read* a
serial number and report it as a probed fact next to the declared one; as with
specs, the two are never merged, and a mismatch is information.

### 3. Managed units

What a host runs that Cerberus can see and act on is a **unit**: `id`, `kind`
(`service`, `container`, `launchd`, `systemd`, `tunnel`, `process`), `state`,
`since`, and `verbs` (the actions it supports). Units are **derived** data,
measured by the probe, rebuilt by probing, reporting their age and a `stale`
flag. They are not declared and not stored as configuration.

### 4. Actions: plan, approve, execute

Every mutation of a host or unit is an **action**, never a bare verb call.
Verbs: `start`, `stop`, `restart`, and `logs`. `logs` is a read: no approval,
redacted output.

1. `POST /hosts/{name}/units/{id}/actions/{verb}/plan` returns a plan: what
   would happen, on which host and unit, and a **plan hash**. This is a
   separate route, not a body flag, so that an older daemon answers 404 and
   never runs the plain operation (AGENTS.md, "A request field that restricts
   or changes a mutation must not be ignorable by an older daemon").
2. A human approves out of band, using the existing approval flow and passkey
   assertion. The approval binds to the plan hash (invariants I5 and I6 of the
   security target): execution runs that plan or fails.
3. `POST …/actions/{verb}` with `approval_id` executes. `approval_id` is safe as
   a body field because a daemon that ignores it predates the gate it
   satisfies.

An **action record** persists `id`, host, unit, verb, plan, plan hash, requester
(identity and kind), approval id, state (`planned`, `approved`, `running`,
`succeeded`, `failed`, `expired`), timing and a redacted result. Tachyon reads
these to show pending approvals and history. The audit log (hash-chained,
append-only) records the same lifecycle.

Refusals: an action targeting the Outpost, or the Cerberus daemon, that carries
the request is refused (the self-mutation guard, extended). Policy defaults
follow the security target: `lifecycle` is `approve` for humans and agents
alike; `read_sensitive` for agents follows that document.

### 5. Surfaces

The same single runtime service as everywhere else: CLI (`cerberus host
units|plan|act|actions`), daemon socket, HTTP API, console and MCP, generated
together. Reads:

```
GET /hosts            GET /hosts/{name}            GET /hosts/{name}/units
GET /hosts/{name}/actions            GET /capacity
```

Tachyon is a **client** of that API and holds no authority of its own: every
write it initiates is an approval-gated action. MCP results carry anything an
agent must act on in the content text, not `_meta`.

**Assumption:** Tachyon reaches Cerberus's HTTP API directly and can present the
passkey approval (or hand it to the console). If it goes through another
service, that service must forward the requester identity and may not approve on
the requester's behalf.

### 6. The on-host agent question

Decision for `CW-20261011-0065`: **no Cerberus-owned daemon on managed hosts.**
The Outpost is the on-host presence; Cerberus contributes a plugin, not a
binary, an update path and a credential of its own. Polling over ssh remains the
baseline and the fallback.

## Alternatives considered

- **A dedicated Cerberus host agent.** A new binary, a new update path and a new
  credential to protect on every machine, duplicating the pairing and transport
  the Outpost already provides.
- **Reusing each machine's own Cerberus (H4).** No new binary, but it makes
  every Cerberus a peer controller, and answers "restart that unit on that
  machine" with a second copy of policy and approval to keep consistent.
- **Policy in the Outpost plugin.** Rejected: a thin executor keeps one gate.
- **Direct restart/stop endpoints for Tachyon.** Rejected: it would make the
  UI an authority, and an approved plan could be swapped before it ran.
- **Asset data in a separate system.** Rejected for now: the Host is already the
  record of the machine, and the asset group is plain declared data.

## Consequences

- Host gains an asset group, a transport, and derived units; a new action
  record store and routes are added. None changes how hosts are used.
- The `outpost` transport is blocked on the Outpost contract; everything else
  can be built and exercised over ssh first.
- Action records and plan hashes make Tachyon's approval UX possible without
  trusting the UI.
- Real serial numbers, hostnames and purchase data stay in `hosts.yaml`, never
  in this repo.

## Open questions

- The Outpost plugin contract and the pairing handshake: does one pairing cover
  every plugin on it, or does each plugin need its own scope?
- Retention and display of action records (how long, and whether results are
  stored or only referenced).
- Whether `disposed_at` hosts leave the default `host list`.
- Bulk actions (restart everything on a host) and whether they are one plan or
  many.

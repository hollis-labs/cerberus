# Policy

> **Status: pre-release.** Policy runs in **shadow mode**: every decision is
> recorded, and nothing is refused on its account yet. Enforcement arrives
> with approvals in a later release.

Cerberus authorizes every operation it runs against a policy, on every
surface: the CLI, the daemon socket, the web console and MCP. The decision is
one of these:

| Decision | Meaning once enforced |
|---|---|
| `allow` | Runs. |
| `dry_run_only` | The preview runs; the real run is refused. |
| `approve` | Runs once someone approves it. For a human in a terminal, that is a typed confirmation that shows the plan. |
| `deny` | Refused, with a reason. |

**The most restrictive matching rule wins** (`deny > approve > dry_run_only >
allow`). There is no rule order to get wrong, and every decision can be
explained by listing the rules that matched.

## Layers

1. **Baseline**, by effect class and caller kind:

   | effect | human | agent |
   |---|---|---|
   | read | allow | allow |
   | read_sensitive | allow | approve |
   | write | approve | approve |
   | lifecycle | approve | approve |
   | destructive | approve | approve |
   | exec | approve | approve |
   | admin | approve | deny |
   | *no effect declared* | deny | deny |

   A human's `lifecycle` operation on a local resource labelled `env: dev` is
   `allow`, so day-to-day local work does not prompt. Automation is read as an
   agent.

2. **Target defaults**, from each resource's labels (see the setup guide's
   "Label Every Resource For Policy"):
   - `admin: owner` denies write, lifecycle, destructive and exec. The owning
     team administers it. Use a per-kind admin, such as
     `admin: { default: owner, docker: self }`, where you run part of it.
   - `admin: shared` requires approval for destructive and exec.
   - `env: prod` requires approval for write and lifecycle, and out-of-band
     approval for destructive.
   - **Unknown reads strictly.** An undeclared admin is read as `owner`, and
     an undeclared env as `prod`.
   - An **ad hoc** target, one named by connection settings such as
     `--host` rather than by a registered resource, is denied unless the
     caller holds the `adhoc_targets` grant. Humans hold it by default;
     agents do not.

3. **Your policy files**, `~/.cerberus/policy/*.yaml` and
   `~/.cerberus/policy/providers/*.yaml`:

   ```yaml
   version: 1
   providers:
     cloudflare:
       rules:
         - id: zones-by-hand
           ops: [create_zone]
           decision: deny
           reason: Zones are created by hand.
   targets:
     - match: { env: prod, tags: [billing] }
       rules:
         - effect: [write, lifecycle]
           principal: { kind: agent }
           decision: deny
   principals:
     - match: { kind: agent, client: "ci-runner*" }
       grants: [adhoc_targets]
       rules:
         - effect: [read_sensitive]
           decision: approve
   ```

   A target `match` field may be negated with `!` (`owner: "!self"`), and
   `unknown` is a value like any other. Because the most restrictive match
   wins, an `allow` rule never loosens anything. To loosen a baseline cell,
   override it:

   ```yaml
   baseline:
     by_effect:
       write: { human: allow }
   ```

   A plugin's suggested policy is copied into
   `providers/<plugin>.yaml` when you accept its install review. It is a
   working file like any other, and applies only when you run
   `cerberus policy apply`.

## Commands

```bash
cerberus policy explain kubernetes.delete_pod --target dev-cluster --as agent
cerberus policy explain local.deploy --target notes-api            # as this terminal is classified
cerberus policy explain docker.stop --adhoc --as agent --working   # against your unapplied files
cerberus policy apply                                              # interactive
cerberus policy report --since 2026-09-01                          # what would be blocked
```

- **`explain`** prints the decision and every rule that matched. It uses
  the applied snapshot, or the working files with `--working`.
- **`apply`** runs only from an interactive terminal. It samples every
  declared operation, against every registered resource of its connector
  plus an unregistered and an ad hoc target, for a human, an agent and
  automation. It prints the decisions that change and applies when you type
  the confirmation. It writes `applied.yaml` and its hash, `applied.sha256`,
  and records the apply in the audit log. Policy never changes through the
  socket, the web console or MCP.
- **`report`** summarizes the recorded decisions that would be blocked,
  grouped by operation, target, caller and deciding rule. This is the list to
  work through before enforcement.

## The snapshot is checked

Only the applied snapshot decides, never the working files. On every load,
the snapshot is compared with the hash `apply` recorded. A snapshot that no
longer matches is not used: the baseline decides, every decision is marked
`snapshot: mismatch`, and the load is recorded as `policy_snapshot_changed`.
Run `cerberus policy apply` again to restore it.

This makes a change **detectable, not impossible**. A process running as your
user can edit anything under `~/.cerberus`.

## In the audit log

Every recorded operation carries its decision:

```json
"policy": {
  "decision": "approve",
  "matched_rules": [{"rule": "baseline.lifecycle.agent", "decision": "approve", "reason": "…"}],
  "would_block": true,
  "snapshot": "sha256:…",
  "shadow": true
}
```

The error codes `policy_denied`, `approval_required`, `approval_pending`,
`approval_expired` and `plan_stale` are reserved for enforcement. Nothing
returns them yet.

## Approvals

Once enforcement is on for an operation, which a later release switches on
scope by scope, an `approve` decision becomes an **approval request**. The
daemon holds requests in `~/.cerberus/approvals/events.jsonl` (mode 0600,
append-only, replayed at start), and each one moves through a lifecycle:

- `pending` → `approved`, `denied`, or `expired`;
- `approved` → `consumed` (used by exactly one call), `expired`, or `revoked`.

Every transition is an audit record: `approval_requested`,
`approval_decided`, `approval_consumed`, `approval_expired` and
`approval_revoked`. The caller gets `approval_pending` with the approval's
id, expiry and the command that decides it.

```bash
cerberus approvals list [--status pending]
cerberus approvals show <id>
```

With the daemon down, these read the store directly. The in-process CLI
without a daemon can only confirm a call on the terminal itself. A call that
needs out-of-band approval is answered with how to start the daemon.

### Plans, and using an approval

An approval is for one **plan**: what the call would do, hashed. The plan
names the operation, the resolved target with its labels, a keyed digest of
the arguments (never the arguments), the operation's dry-run preview where it
has one, and for a plugin, the digests of the binary and the config that would
run. A deploy profile's plan is its steps as they would run, with the
variables each step is given named but never valued, the profile's
definition, and the commit and dirty flag of the checkout it deploys. A plan
never contains a credential value.

The plan is computed when the approval is asked for and again when it is
used, by the same function. Once an approval is decided, retry the call with
exactly the same arguments and its id:

```bash
cerberus connectors exec <connector> <operation> --arg … --ack --approval <id>
```

The approval is spent before anything runs, so it runs at most once. The
outcome's audit record carries `approval_id` and `plan_hash`. A retry is
refused, and nothing runs, when:

- the approval has expired: `approval_expired`;
- anything in the plan changed, such as an argument, the target's labels, the
  preview, or the plugin binary: `plan_stale`. Ask again without the id;
- it was already used, denied or revoked: `approval_required`;
- it belongs to another caller: `approval_required`. An approval is the
  requester's. The principal's kind and channel must match, and so must its
  session where the request carried one.

To see the plan and its hash without running anything:

```bash
cerberus connectors plan <connector> <operation> --arg …
```

This is recorded like a dry run. It runs the operation's preview, and for a
plugin that means a call to the plugin, so a plan is computed only when you
ask for one or when an approval is asked for or used.

**The store is not trusted on its own word.** Anything running as your user
can edit it. An out-of-band approval therefore carries proof that a person
was present, and that proof is verified again when the approval is used, so a
forged "approved" line lets nothing through.

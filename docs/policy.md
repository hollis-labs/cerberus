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

### Deciding an approval

```bash
cerberus approvals approve <id>
cerberus approvals deny <id>
cerberus approvals revoke <id>     # withdraw an approval before it is used
```

These run only on an interactive terminal, never from a script or an agent.
`approve` shows the request in full, including who asked and the plan hash,
and is confirmed by typing the target's name. The console's Approvals page
does the same.

The surface a request came from can never approve it. A request made over MCP
is approved on the terminal or the console, and an MCP client never approves
anything. Anyone can deny.

### Out-of-band approval, with a passkey

A request whose target is production or has no env label, has a shared
admin, or is owned by someone other than you is approved **out of band**: on
the console, with a passkey (Touch ID or a security key) enrolled for this
Cerberus. That is proof a person was there, and an agent running as you
cannot produce it. For such a request, `cerberus approvals approve <id>`
prints and opens a one-time link to its page on the console.

Enroll a passkey once, from a terminal:

```bash
cerberus web                         # the console, if it is not running
cerberus approvals enroll [--label "laptop touch id"]
cerberus approvals keys              # what is enrolled
cerberus approvals keys remove <fingerprint>
```

`enroll` prints and opens a console link, good once and for ten minutes,
where the browser creates the passkey. The first key is trusted on first use.
After it, adding a key or removing one has to be confirmed with a key that is
already enrolled. Passkeys work on `localhost`, so the console's links use
`http://localhost:<port>`. `--listen` names a console on another port.

Every enrollment is recorded in the audit log and raises a notification.
`cerberus status` shows it for a day, so you notice an enrollment you did not
make. Until a key is enrolled, `status` and the console header say that
out-of-band approval is not set up.

The key registry is checked against the audit log. If it is changed any other
way, for example by editing the file, out-of-band approvals are refused for 24
hours. `status` and the console show the cool-down and when it ends.

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

A resource verb's plan is the resource's definition (as a keyed digest, since
its environment can carry values), the build output `apply` and `sync` would
install with its sha256, the checkout's commit and dirty flag for `deploy`,
the launch agent `apply` and `deploy` would write (as a keyed digest), and the
resource's observed state. An approval to stop a service does not stop it
after it has been redefined or restarted as something else.

```bash
cerberus resource plan <id> deploy|apply|reload|stop|sync|remove
cerberus resource stop <id> --ack --approval <id>
```

A pipeline run's plan is every action of every stage in order (a shell
command with its directory and the names, never the values, of the
environment variables it inherits, or the resource verb), and the pipeline's
definition and each resource it names, as keyed digests. Each action that
changes a resource (build, deploy, start, stop) also carries that verb's own
plan, exactly as `cerberus resource plan` would compute it, so a pipeline that
deploys a resource binds that deploy's checkout, build output and launch
agent. An approved run
executes the definition the plan was checked against, not the config as it
reads a moment later.

```bash
cerberus pipeline plan <id>
cerberus pipeline run <id> --ack --approval <id>
```

A plan is asked for on its own route. A daemon that predates plans refuses
the request rather than running the verb, and the CLI says to restart it.

### Confirming on your own terminal

When policy wants a `tty_confirm` approval and you run the command yourself
in an interactive terminal, you don't need a second command. Cerberus shows
the plan: the effect, the target with its labels and which process computed
the plan in bold, then what would run and the full plan hash. It asks you to
type the target:

```
Type the target (notes-api) to confirm, or anything else to cancel:
```

`y` is not a confirmation. Typing the target sends the call again on its
confirm route with the hash of the plan you were shown. That one call
decides the approval the first attempt asked for, spends it, and runs, so
nothing is left pending. If the plan changed in between, the call is
refused as `plan_stale` and nothing runs.

A confirmation is refused, and the approval is left as it was, when:

- the target is `env: prod`, shared or not yours, or the rule asks for out
  of band. Those are approved with `cerberus approvals approve` and a
  passkey;
- the caller is not a person at the CLI: an agent, an MCP client, or a
  command whose stdin or stdout is not a terminal;
- the approval named belongs to another caller or is no longer pending.

`--ack` is still needed where the operation requires it. A confirmation
doesn't replace it. Without a daemon, a confirmation is recorded in the
audit log (requested, decided and consumed) under an id of its own, since
nothing holds it.

**In the console**, the confirm dialog does the same thing for resource
actions, pipeline runs, deployment-profile runs and connector operations.
It shows the effect, target, labels and which process computed the plan in
bold, then what would run and the full plan hash, and it enables **Confirm
and run** only once the target is typed. Only a signed-in console session
can confirm. The console marks its session on the request after checking
the cookie, and a web request without that session is refused. An approval
that must be met out of band sends you to the approvals page instead.

**This is a floor, not a boundary.** "A person at the CLI" is what the
terminal says about itself, so a program driving a pseudo-terminal could
claim it. The same goes for "a console session" as the daemon hears it,
since the console's claim travels over the socket. Confirming on the call protects against an agent that follows
the rules. Out-of-band approval with a passkey is the boundary.

**The store is not trusted on its own word.** Anything running as your user
can edit it. An out-of-band approval therefore carries proof that a person
was present, and that proof is verified again when the approval is used, so a
forged "approved" line lets nothing through.

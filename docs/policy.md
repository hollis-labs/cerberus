# Policy

> **Status: pre-release.** Policy runs in **shadow mode** until you switch
> it on: every decision is recorded, and nothing is refused on its account
> except in the scopes you enforce with `cerberus policy enforce`.

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
cerberus policy report --scope principal=agent,env=prod            # ...within one enforcement scope
```

- **`explain`** prints the decision and every rule that matched. It uses
  the applied snapshot, or the working files with `--working`.
- **Every policy file is read strictly.** An unknown key is an error, and so
  is a value outside its vocabulary: decision, effect, principal kind,
  approval channel and scope, `env` and `admin` in a match, posture, and
  grants. A typo can't silently widen or weaken a rule. A misspelled `ops`
  on an allow rule, read loosely, would allow every operation. `apply` and
  `explain --working` report the problem, and an applied snapshot that fails
  to read falls back to the baseline.
- **`apply`** runs only from an interactive terminal. It samples every
  declared operation, against every registered resource of its connector
  plus an unregistered and an ad hoc target, for a human, an agent and
  automation. It prints the decisions that change and applies when you type
  the confirmation. It writes `applied.yaml` and its hash, `applied.sha256`,
  and records the apply in the audit log. Policy never changes through the
  socket, the web console or MCP.
- **`report`** summarizes the recorded decisions that would be blocked. This
  is the list to work through before enforcement, and the data for choosing
  what to enforce first.
  - Each decision is made again under the policy applied now, and one the
    current policy no longer blocks is counted apart.
  - Decisions are grouped by deciding rule, caller kind and target, and each
    group lists its operations.
  - Each group names the approval it would need and its channel: a
    confirmation on your terminal, or out of band with a passkey. It also
    says whether that channel is ready. Out of band needs the daemon and an
    enrolled passkey outside a cool-down. An agent's terminal approval needs
    the daemon to hold it. A rule asking for two approvers can't be met yet.
    The report flags every decision that enforcing now would refuse with no
    way to approve.
  - `--scope` narrows the report to what one enforcement scope would cover,
    with comma-separated `key=value` terms: `principal`, `env`, `effect`,
    `connector`, `target` and `rule`.

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

## Switching enforcement on

Until you switch it on, policy runs in **shadow**: every decision is recorded
and nothing is refused. `cerberus policy report` shows what policy would have
blocked, and what enforcing it would need. When the shadow data says a scope
is ready, enforce it:

```bash
cerberus policy report --since 2026-09-01 --scope principal=agent,env=prod
cerberus policy enforce --scope principal=agent,env=prod --id agents-prod
```

`policy enforce` runs on a terminal. It shows the report for the scope over
`--since` (seven days by default), including the would-blocks that would now
be refused with no channel ready to approve them. It then shows the
enforcement before and after and asks for a typed confirmation. It writes
`enforcement.yaml`, the working file it owns, and applies through the same
snapshot path as `policy apply`:

```yaml
enforcement:
  mode: shadow          # or enforce: everything
  enforce:
    - id: agents-prod
      match: { env: prod }
      principal: agent  # or "!human"
      effect: [destructive, exec]
```

A scope is enforced; everything else stays shadow. `--all` enforces
everything, and `--off` returns a scope, or with `--all` the mode, to shadow.
Every change is recorded (`enforcement_changed`, and the apply record
carries the section), notifies, and shows in `cerberus status`, `posture show`,
`policy explain` and the console header. Changing policy itself is never
enforced: it has its own terminal gate, and enforcing it would lock the way
back to shadow.

If the applied snapshot fails its hash check, the baseline decides and
enforcement doesn't silently switch off. Cerberus enforces the last
verified enforcement, read from the hash-chained audit log (the newest
apply's record). If that can't be determined, because the chain doesn't
verify or no apply is recorded, it enforces everything. Either way it says
so loudly, and `cerberus policy apply` puts it right.

## Egress: what comes back

Results label the text in them. `untrusted` is text Cerberus didn't compose:
logs, command output, and names or descriptions a vendor or anyone with push
access can set. `personal` is personal data. Over MCP, a labeled result says
where that text is, both in `_meta` and in a second content block. By default
everything is delivered, labeled.

An `egress:` section shapes labeled output for a caller and target:

```yaml
egress:
  - id: agent-logs-off-dev
    match: { env: "!dev" }        # a target match, as in targets:
    principal: { kind: agent }
    effect: [read_sensitive]      # optional: only operations of these effects
    label: untrusted              # untrusted | personal
    action: cap                   # pass | cap | mask | refuse
    lines: 200                    # cap only: lines of text, or list entries
    mode: enforce                 # shadow (the default) | enforce
```

- **Shadow first.** A rule without `mode: enforce` changes nothing. Each
  outcome record carries an `egress` entry saying which rule matched, where,
  what it would have done and how much it would have withheld.
- **Enforced.**
  - `cap` keeps the first lines and adds "[cerberus: N more lines withheld
    by egress rule …]".
  - `mask` replaces the text with a note of what was masked and by which
    rule.
  - `refuse` on a read answers `egress_refused`, saying which rule withholds
    which label. On anything that isn't a read, the operation has already
    run, and an agent told it failed would run it again. So there `refuse`
    reports success with the output replaced by a note naming the rule, and
    the outcome records `refuse→withheld`. `policy explain` and
    `policy apply` warn about any refuse rule that can match such an
    operation; `effect: [read, read_sensitive]` limits it to reads.
  - Nothing is dropped silently.
- **Most restrictive wins.** `refuse`, then `mask`, then `cap` (the smaller
  limit when two apply), then `pass`. The rule enforces if any matching rule
  at that strength does.
- **Where it applies:** connector and plugin results, resource logs,
  resource deploy/apply/sync results (their build and install output), and
  pipeline runs (their stage and run errors). Plugin output without labels is
  untrusted as a whole. A typed result that a refusal withholds keeps its
  type and its success. Each labeled field reads "output withheld by egress
  rule …", so a caller never reads a success as a failure.
- **`policy report`** ends with an egress summary from the outcome records:
  each rule, label, action, principal and target, how often, how much it
  withheld, and whether it applied or would have (shadow).

Approving before reading is separate from this and already in place:
`read_sensitive` operations need approval for agents under the baseline.

## Approvals

Once enforcement is on for an operation, which you switch on scope by scope
with `cerberus policy enforce` (below), an `approve` decision becomes an **approval request**. The
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

Deploy-profile runs are asked for, confirmed and run by the daemon, like
resource verbs. The console is a client, so the approval lives with the
broker. A profile carries `env`, `owner`, `admin` and `tags` like a
resource. An unlabeled profile reads as unknown and needs out-of-band
approval. `make smoke-confirm` drives the dialog in headless Chrome
against a scratch daemon.

**This is a floor, not a boundary.** "A person at the CLI" is what the
terminal says about itself, so a program driving a pseudo-terminal could
claim it. The same goes for "a console session" as the daemon hears it,
since the console's claim travels over the socket. Confirming on the call protects against an agent that follows
the rules. Out-of-band approval with a passkey is the boundary.

### Grants: approving more than one call

An approve rule can ask for a **grant** instead of a single-use approval:

```yaml
targets:
  - match: { env: dev, owner: self, admin: self }
    rules:
      - { id: dev-restarts, ops: [reload], decision: approve,
          approval: { scope: window, ttl: 30m } }
```

- `once` (the default) is spent by one call and bound to its plan.
- `window` covers every call of the same operation, on the same target, by
  the same requester (the same kind and channel, so an agent's grant never
  covers your own call, or the reverse), for the TTL from when it was
  approved.
- `session` also binds the requester's session. With no session to bind to,
  it is a once approval.

Once approved, a grant needs no approval id: a later call it covers runs
under it, and each use is recorded as `grant_used`. Approving one records
`grant_created`. A grant is re-checked on every use:

- it never widens a `deny`;
- a rule narrowed to `once`, or to out of band where the grant was met on
  a terminal, is not covered by the grant it gave;
- an out-of-band grant's passkey proof is verified again.

Revoke a grant with `cerberus approvals revoke <id>` or from the console's
approvals page (`grant_revoked`). `cerberus status` and the approvals page
list every active grant.

Grants are allowed wherever your policy allows them. On a prod, shared or
not-ours target that is your choice, and it is loud. `policy apply` and
`policy explain` flag any rule that could give one there, `cerberus status`
marks each active one with `!`, and every use carries
`grant_on_protected_target: true`.

### Break glass

When policy asks for an approval you can't get in time, you can break glass
on your own call:

```bash
cerberus resource stop <id> --ack --break-glass --reason "the incident needs it now"
```

Cerberus shows the plan under a **BREAK GLASS** banner and asks you to type
the target. The call then gets past the `approve`, whatever channel or number
of approvers it asked for. It **never gets past a `deny`**.

Break glass requires all of these:

- an interactive terminal;
- a person at the CLI (not an agent, MCP or the console);
- a reason;
- the target typed;
- a running daemon, which keeps its record.

On a prod, shared, not-ours or unlabeled target, a terminal isn't enough.
The call asks for a passkey instead: approve it on the console, where it is
labeled BREAK GLASS with its reason and plan, then run the command again with
`--break-glass --approval <id>`.

It is loud:

- a `break_glass` audit record is written before the call's intent;
- the operator gets a desktop notification;
- the console header shows a red badge for a day;
- `cerberus status` lists it until someone acknowledges it with
  `cerberus approvals ack-break-glass <id>` on a terminal.

Break glass is limited per target: by default 3 uses per rolling 24h, set in
policy with `break_glass: {per_target: 3, window: 24h}` and changed with
`cerberus policy apply`. `policy explain` and `posture show` print the limit.

### Lockdown and freeze: the emergency brake

When something is going wrong and you want Cerberus to stop acting, engage
the brake:

```bash
cerberus lockdown --reason "an agent is looping"   # everything but plain reads
cerberus freeze --scope env=prod --reason "..."    # only the targets a scope selects
cerberus freeze list
```

A **lockdown** refuses every operation except a plain `read`. It refuses
`read_sensitive` too, so logs and command output stop as well. A **freeze**
does the same only for the targets its `--scope` matches. The scope keys are
`id`, `connector`, `kind`, `env`, `owner`, `admin` and `tag`.

Engaging is meant to be trivially easy:

- **one command, with no confirmation.** It works with the daemon down: the
  CLI writes the brake store itself, and the daemon reads it when it starts.
- **the Lockdown button in the console header**, with an optional reason
  and no typed phrase;
- **the `cerberus_lockdown` MCP tool**, so an agent can stop itself. The tool
  can only engage a lockdown. It is the one tool policy never gates.

The brakes run before policy, in every enforcement mode, shadow included.
A refused call answers `lockdown` or `frozen` (HTTP 423). The refusal says
who engaged the brake, when and why, and how to lift it. Dry runs and plans
still work, because they run nothing. Policy changes, approval decisions and
the brakes themselves are never braked, so the way back stays open.

It is loud:

- a red banner on every console page;
- `!!! LOCKDOWN` and `!!! FREEZE` lines at the top of `cerberus status`;
- a `Brakes:` line in `policy explain`;
- a `brake_changed` audit record and a desktop notification for every change.

Background work under a brake:

- **Auto-restarts.** Under a lockdown the monitor keeps restarting declared
  services, because pausing them would turn an incident into an outage.
  Under a freeze that covers a resource, its restarts pause. Each skipped
  restart is logged.
- **Pipelines** don't run under either kind of brake.

**Lifting is a person's act.** Run `cerberus lockdown --off` or
`cerberus freeze --off <id>` on an interactive terminal, and type the phrase
it asks for. An agent or an MCP client can never lift a brake. If a passkey is
enrolled, the lift is also approved with the passkey: the first `--off`
prints a console link, and you approve it there. Then either click **Lift
now** on the approval, or run the command again with `--approval <id>`. The
console's own Lift button takes you to the same approval. With no passkey
enrolled, the terminal and the typed phrase are the floor. That floor applies
only when the passkey registry opens and holds no key. If the registry can't
be read, or it changed outside `cerberus approvals enroll` (it was deleted,
say), the lift is refused until the registry is repaired or the cool-down
ends. It never drops to the floor. Lifting needs the daemon, which is what
checks for an enrolled passkey.

The brake store is `~/.cerberus/brakes/`. It is hash-chained, like the other
stores. The daemon trusts the store only as far as the verified audit log
agrees: if the store and the newest `brake_changed` record disagree, the
more restrictive of the two applies. Deleting the store therefore doesn't
lift a lockdown the audit log recorded.

### When an agent asks

An agent working through MCP can't approve anything. There is no MCP tool that
decides an approval, and the daemon refuses a decision from an MCP client
whatever it claims to be. When one of its calls needs approval, the tool
answers with an error the agent can act on:

```json
{
  "success": false,
  "code": "approval_pending",
  "error": "docker stop needs approval (tty_confirm, rule …): approval apr_… is pending until …",
  "approval": {
    "id": "apr_…",
    "expires_at": "…",
    "channel": "tty_confirm",
    "approve_with": "cerberus approvals approve apr_…"
  },
  "next_step": "ask your operator to run `cerberus approvals approve apr_…` in their terminal, …"
}
```

`next_step` says what to ask you for. For an out-of-band approval, that's a
passkey on the console. The agent then calls `cerberus_approval_wait` with the
id, which returns when the approval changes state or after at most a minute.
Once you have approved it, the agent retries the same call with
`approval_id`. The approval is the agent's own: it runs that call once, with
those arguments, for that caller. `expired`, `plan_stale` and
`policy_denied` answers carry a `next_step` too.

**The store is not trusted on its own word.** Anything running as your user
can edit it. An out-of-band approval therefore carries proof that a person
was present, and that proof is verified again when the approval is used, so a
forged "approved" line lets nothing through.

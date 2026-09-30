# Plugins: install review

> **Status: pre-release.** The plugin contract and the commands below may still
> change before a tagged release.

A connector plugin is a separate program that Cerberus starts and talks to over
the plugin SDK's subprocess protocol. It runs with your authority: it gets the
credentials it declares, and whatever access its code has. Cerberus does not
sign, vet or vouch for plugins, including our own. What it does is make the
choice to run one an informed one:

- **A declaration.** `plugin.yaml` says what the plugin can do: its operations,
  their effect classes, their previews, the secrets and host capabilities it
  wants, and the policy and exposure it suggests for itself.
- **A review at install.** Cerberus reads that declaration without starting the
  plugin, shows it to you with its gaps, and installs the plugin only when you
  type its id.
- **Enforcement at load.** Every load checks that the plugin is still the bundle
  you reviewed.

## Installing

```bash
cerberus connectors plugin managed install ./dist/myplugin
```

This runs in your terminal, not in the daemon, and **only from an interactive
terminal**. It is refused when stdin or stdout is not a terminal, and there is
no socket, web console or MCP route that installs a plugin. An agent cannot
install a plugin, grant it a capability or accept its suggested policy.

The review looks like this:

```text
Plugin: myplugin 0.3.0 (from /home/you/src/myplugin/dist/myplugin)
Entrypoint: bin/myplugin  sha256:4be1a0c2d9e3
Bundle:     sha256:9f0c...

Operations (6)
  read              3   list_things, get_thing, get_status
  read_sensitive    1   get_logs   -> free text, may carry secrets or personal data
  write             1   update_thing
  lifecycle         0
  destructive       1   delete_thing
  exec              0
  admin             0
Previews: server on 2 (delete_thing, update_thing)
          -> claimed by the plugin; Cerberus cannot verify them. Accepting this review lets
             a dry run of these operations run without --ack.
Secrets:  token (required)
Capabilities requested: none
Suggested policy: 1 rule(s) (delete_thing: approval)
                  -> shown only; Cerberus does not apply a plugin's policy
MCP exposure requested: 3 operation(s): get_status, get_thing, list_things
                        -> nothing reaches MCP until you list it under mcp.expose in connector-config.yaml

Gaps
  ! no telemetry declared for update_thing: its audit record will carry the host's view only

Accept? Type the plugin id (myplugin) to confirm:
```

**Gaps are shown, not refused.** Each gap is read the strict way. An operation
with no effect class is treated as `exec`, so every call needs acknowledgment.
An undeclared preview is `none`, and an unlabelled output is free text.

When you accept:

1. The review is recorded in the audit log before anything changes. The record
   holds the digest of the summary you were shown, the bundle digest, the gaps
   and, for a re-review, the diff. If the audit log cannot be written, nothing
   is installed.
2. The bundle is copied into `~/.cerberus/plugins/<id>/<digest>/`, and the
   plugin runs from that copy. What is copied is the staged copy the review was
   built from, so a source directory that changes while you read the review
   cannot change what is installed. Rebuilding your checkout changes nothing
   that runs until you install again.
3. The entry is written to `~/.cerberus/plugin-connectors.json`, and the running
   daemon is asked to reload the plugin by id. If the daemon is not running, it
   picks the entry up when it starts.

A fresh install is registered, not started. Start it with
`cerberus connectors plugin managed load <id>`.

The bundle digest covers every file in the plugin directory: its relative path,
its executable bit and its content. Editing `plugin.yaml` is a change in the
same way a rebuilt binary is. A bundle containing a symlink or any non-regular
file is refused, and bundles are capped at 512 MiB.

## Every load checks the bundle

Each load recomputes the digest of the installed copy and compares it with the
one you accepted. A plugin that no longer matches is refused with
`plugin_changed`, and nothing is started:

```text
plugin "myplugin" is not the plugin you reviewed: its bundle digest is sha256:1c2d...,
and the accepted review is for sha256:9f0c.... Nothing was loaded. To see what changed
and accept it, run `cerberus connectors plugin managed load myplugin --accept-changes`
in a terminal
```

This applies on every surface: the CLI, the web console, the daemon's restore at
startup, and a reload. A refused reload leaves the running plugin as it was.

`--accept-changes`, from an interactive terminal, shows the summary again with a
diff against the review you accepted, and loads the plugin once you type its id:

```text
Changes since the review accepted 2026-09-25T17:02:11Z:
  ~ operation update_thing effect write -> exec
  + secret admin_token
```

## Upgrading

Installing a plugin that is already installed is an upgrade, and an upgrade is a
re-review. New operations, changed effect classes or previews, new secrets or
capabilities, and changed suggested policy or exposure are shown as a diff.
Nothing new reaches the daemon until you accept it. If the new build is
byte-for-byte the installed one, there is nothing to review.

## Plugins installed before install review

A plugin installed before install review existed has no accepted review. It
keeps loading exactly as before, reported in `managed list` with
`"review_pending": true`. Until it is reviewed:

- its bundle is not compared on load, because there is no accepted digest to
  compare with, and it runs from the directory it was installed from;
- its previews are not accepted, so its dry runs still need `--ack`.

Review each one once:

```bash
cerberus connectors plugin managed review <id>
```

This is the same review install shows, with the same typed confirmation. It is
not a rubber stamp. Accepting it copies the plugin into the store, and from then
on the plugin is checked on every load like any other.

## Previews are the plugin's claim

A dry run of a plugin operation reaches the plugin, which is trusted to honor
`dry_run`. Cerberus cannot verify that. Accepting a review accepts the plugin's
preview declarations: a dry run of an operation whose preview you accepted runs
without `--ack`. The real run still needs `--ack`. Every plugin dry run is
recorded in the audit log with `"preview": "plugin_claimed"`.

## Development installs (`--dev`)

`--dev` exists only in devmode builds (`go build -tags devmode`). **A release
build refuses it** with "a development install (--dev) requires a devmode build".

A development install is reviewed like any other. It differs in two ways:

- It runs from its source directory rather than a store copy, so a rebuild is
  picked up without reinstalling.
- Its destructive operations are refused, acknowledged or not.

It loses only the copy, not the check. Every load still compares the bundle
digest, so a rebuilt dev plugin is refused with `plugin_changed` until you run
`load <id> --accept-changes`. That re-review on every change is the price of
running code from a working directory.

`cerberus connectors plugin exec <dir> <operation>` and `plugin health <dir>`
run a plugin directory once, in a throwaway host in your own shell, with no
install and no review. That is in-process power: it is exactly as much as
running the binary yourself. It is not available over the socket, the web
console or MCP.

## What a plugin declares

`plugin.yaml` carries the connector manifest and, beside it in the `cerberus:`
block, the review declarations. Every declaration is the plugin's claim about
itself. Cerberus shows it, uses it only to narrow what the plugin can reach, and
never widens anything on its word.

A plugin's connector id can't be one Cerberus serves or keys its own records
and gates on, so install refuses these:

- the built-in connectors: `ssh` and `docker`;
- `local`, the supervision lane;
- `policy`, `brake`, `approvals`, `audit`, `console`, `pipeline`, `mcp-http`,
  `plugin` and `secrets`.

A plugin with one of those ids would shadow the host's connector, or skip the
brakes and enforcement. Its records could also pass for a policy apply or an
audit prune.

```yaml
schema_version: "1"
id: myplugin
version: 0.3.0
protocol: plugin-sdk/subprocess
runtime: subprocess
entrypoint:
  command: bin/myplugin
cerberus:
  connector:
    # ... the connector manifest: operations with effect, preview, output,
    # cost, local_fs and target; config fields; secrets
  host:
    min_contract: 1
    max_contract: 1
  suggested_policy:
    - operation: delete_thing
      require: approval        # ack | approval | approval_for_agents | deny
      reason: deletes cannot be undone
  surfaces:
    mcp: [list_things, get_thing, get_status]
    cli_only: [get_logs]
  telemetry:
    - operation: update_thing
      events: [step, change, warning]
```

| Declaration | What the host does with it |
|---|---|
| `host` | The range of Cerberus contract versions the plugin was built for (`pkg/plugin.ContractVersion`). A host outside the range refuses the plugin at install and at load. Undeclared is a gap. |
| `suggested_policy` | Shown in the review. Never applied. |
| `surfaces.mcp` | Shown as requested MCP exposure. You still opt each operation in under `mcp.expose` in `connector-config.yaml`. |
| `surfaces.cli_only` | Honored: the operation is never exposed to MCP, even if `connector-config.yaml` lists it (a warning says why). |
| `telemetry` | The event kinds an operation reports (`plugin.AttachTelemetry`). A non-read operation without one is a gap: its audit record carries only the host's view. |

A declaration naming an operation the connector does not declare, or using a
value outside the vocabulary, is refused at install.

### Output labels

An operation can describe its result with `output_schema`, a JSON Schema that
only needs to go as deep as its labels. Any property can carry
`x-cerberus-label`: `untrusted` for text Cerberus didn't compose (messages,
names or descriptions a user or a vendor can set), `personal` for personal
data, or both as a list.

```yaml
operations:
  - name: list_workers
    effect: read
    output: structured
    output_schema:
      type: array
      items:
        properties:
          name:  { type: string, x-cerberus-label: personal }
          email: { type: string, x-cerberus-label: [personal, untrusted] }
          note:  { type: string, x-cerberus-label: untrusted }
```

A Go plugin doesn't write the schema by hand. It tags its DTO fields the way
Cerberus's own DTOs are tagged, and derives the schema from the type it
returns, so the manifest can't drift from the DTO:

```go
type Worker struct {
    Name  string `json:"name"  cerb:"personal"`
    Email string `json:"email" cerb:"personal,untrusted"`
}

contract.Operation{Name: "list_workers", /* ... */,
    OutputSchema: contract.OutputSchemaOf[[]Worker]()}
```

An MCP tool result names its labeled fields in `_meta` (`cerberus/untrusted`
and `cerberus/personal`, as JSON pointers), so a client can present that text
as data and not as instructions.

- An operation **without** `output_schema` is unlabeled. It still installs.
  The review lists it as a gap, and its whole result is marked untrusted.
- `output_schema: {}` says the result has been reviewed and nothing in it is
  untrusted or personal.
- An unknown label is refused at install.
- A change to an operation's labels shows in an upgrade's review diff.

## Secret backends

A plugin can claim a secret reference scheme:

```yaml
cerberus:
  secret_backend:
    scheme: op
    reference: "op://<vault>/<item>/<field>"
```

The host then routes `op://` references to it. Only vault reference schemes can
be claimed (today `op` and `keeper`): a scheme whose values can carry a
credential, such as `postgres://user:password@host`, is refused, since the
backend would be sent every such value. It sends them as a plugin-sdk
`command/execute` named `cerberus.secret/resolve`, with `{"ref": "..."}` as the
argument.
- **A success** is action `message` with the value as the content.
- **A failure** is action `error` with a coded payload (the `cerberus_error`
  object a coded tool error carries).

The command is not a manifest operation, so it never becomes a CLI command, an
API operation or an MCP tool.

The review shows the claim first, as a plugin that will see every secret
resolved through that scheme. A backend's own declared secrets resolve through
the core chain only, never through another backend. See `docs/secrets.md`.

## Deadlines and limits

The host supervises every plugin it runs. A plugin that hangs, floods its
output or grows without bound costs you one plugin, not the daemon.

**Every protocol call has a deadline.** Here are the defaults and the host
maximums:

| Call | Default | Maximum |
|---|---|---|
| init, load | 20s | 2m |
| health, unload | 5s | 30s |
| each operation | 2m | 30m |
| a secret backend's resolve | 10s | 1m |

You can override any of them per plugin in `connector-config.yaml`, which is
the file you own. A value over the host maximum is clamped, with a load
warning:

```yaml
my-plugin:
  limits:
    call_timeout: 10m
    operations: { deploy: 20m }   # per operation; the names must be declared
    init_timeout: 60s
    resolve_timeout: 20s          # a secret backend's resolve; default 10s
    max_result_bytes: 4194304     # default 1 MiB, at most 16 MiB
    memory_mib: 4096              # the memory watchdog; default 2048
    open_files: 2048              # RLIMIT_NOFILE; default 1024
    file_mib: 4096                # RLIMIT_FSIZE; default 1024
    cpu_seconds: 0                # RLIMIT_CPU; off by default (see below)
```

A resolve has its own deadline, separate from the operation deadline,
because it blocks whatever needs the value. That includes a dependent plugin's
load, which resolves its declared credentials before Init runs.

Decoding is strict: an older daemon refuses a file that has `limits:`, and
with it every plugin load. Update the daemon before you add limits.

**When a plugin misses a deadline:**

- The host kills its whole process group and restarts it with backoff (1s,
  5s, 30s).
- The call answers `deadline_exceeded`. For anything but a read, the answer
  says the operation may have partly run, so check the target before you
  retry.
- Other calls in flight to that plugin answer `connector_unavailable`.
- After three restarts within ten minutes, the host gives up. The plugin stays
  unloaded until you run `cerberus connectors plugin managed load <id>`. That
  shows up as a notification, a `!!! PLUGIN` line in `cerberus status`, and in
  health and `plugin managed list`.
- A plugin that exits on its own is restarted the same way.
- Every stop and restart is recorded as automation.

No plugin call holds a lock that other plugins wait on. At daemon start,
plugins load in parallel, so a plugin that hangs on init delays startup by one
deadline, not by the sum of them.

**Output is bounded.**

- The host reads at most 16 MiB per message from a plugin. A plugin that
  sends more is stopped, because its stream can't be recovered. For a read,
  the call answers `output_too_large`. For anything else, it answers that the
  operation ran but its reply couldn't be read, so its outcome is unknown.
- Past `max_result_bytes`:
  - text is cut, with a visible marker (`[cerberus: output truncated — …]`);
  - structured output from a read is refused as `output_too_large`, so
    narrow the request;
  - structured output from anything else is replaced with a note saying the
    operation ran and succeeded and its output was withheld. It is never an
    error an agent would retry.
- A plugin's stderr reaches the daemon log at up to 64 KiB a minute. Past
  that, lines are dropped, and one marker line counts them.

**Process limits.** A plugin starts through the cerberus binary's own
`__plugin-exec`, which the daemon finds as its own executable, never on
`PATH`. It sets these rlimits before it execs the plugin:

- open files (`RLIMIT_NOFILE`);
- file size (`RLIMIT_FSIZE`);
- no core dumps, since a core would carry the plugin's credentials.

The plugin runs in its own process group, and every kill reaches the whole
group, so children it forked die with it.

**What macOS can't enforce:**

- **Memory.** `RLIMIT_RSS` is ignored, and `RLIMIT_AS` and `RLIMIT_DATA` are
  unreliable (setting them breaks the Go runtime). Instead, the host checks
  the process group's resident size every 10 seconds and stops a plugin over
  `memory_mib`. That check is best effort: a spike between two checks isn't
  seen.
- **Per-call CPU.** `RLIMIT_CPU` is a lifetime budget for a process that runs
  for days, so it is off by default. The wall-clock deadline is what bounds a
  single call.
- **A per-plugin process count.** `RLIMIT_NPROC` counts every process of your
  user, so it isn't used.
- **A child that escapes.** A child that moves itself to its own session or
  process group escapes the group kill. macOS has no way to prevent that.

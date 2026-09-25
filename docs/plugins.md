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

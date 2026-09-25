---
id: "CERB-CAP-303"
class: "capability"
name: "Daemon-managed plugin lifecycle"
summary: "Installs plugins by an interactive review in the operator's terminal into a digest-named plugin store, and loads, reloads, unloads and uninstalls them inside the daemon, refusing any bundle that no longer matches the review the operator accepted."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "managed service, reviewer, state file and socket routes read on the P1-5 branch; review, reload, migration and plugin_changed tests run"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/cerbapi/managed_plugin_connector_service.go"
tags:
  - "plugin"
  - "lifecycle"
  - "daemon"
  - "socket"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-301"
    note: "wraps the plugin host manager"
  - type: "implements"
    target: "CERB-DEC-356"
    note: "an unrestorable plugin is skipped, never fatal"
  - type: "blocks"
    target: "CERB-GAP-333"
    note: "the held subprocess is unsupervised"
  - type: "blocks"
    target: "CERB-GAP-337"
    note: "the inventory is not a version record"
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "console lacks uninstall and exec; MCP lacks everything"
  - type: "blocks"
    target: "CERB-GAP-849"
    note: "closed in P1-5: install is an in-process review"
  - type: "relates_to"
    target: "CERB-GAP-857"
    note: "an accepted review is a file the operator's uid can write"
---

# Daemon-managed plugin lifecycle

`cerberus connectors plugin managed {install,list,load,unload,uninstall,health,exec}`
drives a plugin host that lives inside the daemon, over the Unix socket at
`~/.cerberus/cerberus.sock`. Install reads and validates `plugin.yaml`, refuses a
reserved connector id, fingerprints the entrypoint, records an install origin
(`installed`, or `dev` on a devmode build) and adds the result to an in-memory
inventory. Load launches the subprocess,
resolves declared secrets, sends `plugin/init`, checks the protocol integer,
sends `plugin/load` and keeps the process. Unload sends `plugin/unload`, closes
stdin, and kills the child after a three-second grace. Uninstall unloads first
if needed, then drops the inventory entry. Every one of those persists
`~/.cerberus/plugin-connectors.json`, whose entries are exactly
`{plugin_dir, options, loaded}`. Before PR #51 the middle field was `trust`;
an old entry still loads, and its `trust` object is read for `dev_mode`
alone.

Restore at daemon start is the part that has already drawn blood. It
re-installs every persisted entry and re-loads the ones marked loaded, and it
treats every failure as a warning rather than an error: a plugin whose directory
has gone — `make clean` in a plugin repo, or a fresh clone with a gitignored
`dist/` — used to return an error from the constructor, which failed the launchd
job, which `KeepAlive` crash-looped, which bricked Cerberus entirely over an
optional component. Now the entry is skipped with a warning naming the recovery
and, crucially, *kept*: since P1-5 the daemon never writes a snapshot of its
view, only the loaded flags of entries it holds, so an entry it could not
restore stays in the file and a rebuilt plugin comes back on the next restart. The
same path skips a plugin whose id has since become reserved.

Two consequences of "the daemon holds the process" are worth stating plainly.
First, the subprocess is unsupervised. `RPCProcess` notices its own exit and
fails pending calls, but the manager only deletes from `m.running` in `Unload`,
so a crashed plugin remains `loaded: true` in `managed list` and returns a
transport error from every operation until someone unloads it by hand. Nothing
restarts it. Second, the inventory is not a version record. There is no pin
and no rollback (CERB-GAP-337). Until P1-5 there was also no comparison against
the hash computed at install (CERB-GAP-336); since P1-5 every load compares the
bundle digest the operator accepted, below.

Surface coverage is uneven and the uneven part is the destructive part. CLI has
all seven verbs. The socket had all seven, with install taking a path
(CERB-GAP-849), until P1-5 retired it. The web console (`web/src/pages/plugins.tsx`) had
install and a health probe of a directory at audit time. PR #50 retired both:
web install-by-path and the directory routes answer 410, the page shows the
`cerberus connectors plugin managed install <dir>` command instead, and managed
exec over the web refuses a `plugin_dir` field. The page keeps list, health by
id and the load/unload toggle. There is no uninstall and no exec on the page. MCP has nothing at all:
none of the 51 tools registered in `cmd/cerberus/cmd_mcp.go` touches a plugin,
and there is no dynamic tool generation, so an agent cannot install, inspect,
load or run a plugin.

Since P1-4a, install, load, unload and uninstall each write an intent and an
outcome to the audit log as `admin` calls on connector `plugin`, and an
unwritable log refuses them (CERB-CAP-604). A plugin operation is recorded once,
by the admin lane, with the plugin's config and entrypoint fingerprints. The CLI
verb that called the direct route, `managed exec`, was retired on 2026-09-25;
`connectors exec` goes through the admin lane.

Since P1-4b the outcome also carries what the plugin reported for that call:
events it returned under `cerberus_telemetry`, which the host strips from the
result, and the stderr lines it wrote while the call ran. Both are bounded and
redacted by the plugin's own value redactor. A plugin can enrich the host's
record; it cannot write one.

## Install is a review (P1-5)

**Install moved out of the daemon.** `managed install <dir>` runs in the CLI
process through `cerbapi.PluginReviewer` (`internal/cerbapi/plugin_review.go`),
which is not on the `Client` interface and refuses any caller surface but
`in_process`. The command itself refuses unless stdin and stdout are a terminal.
It copies the directory into a staging area of the plugin store, digests the
copy, reads its `plugin.yaml` without starting the plugin, and prints the
section 10 review (`pluginhost.BuildReview` and `Render`): operations by effect,
previews marked as the plugin's claim, secrets, capabilities, suggested policy,
requested MCP exposure and gaps. It installs only when the operator types the
plugin id. The accepted review is recorded as an `admin` event before anything
changes, the staged copy is committed to `~/.cerberus/plugins/<id>/<digest>/`,
and a reviewed entry is written to `plugin-connectors.json`. The CLI then asks
the daemon to `POST /plugins/connectors/{id}/reload`. That route takes an id,
never a path, and only re-reads the reviewed entry. The socket's
`POST /plugins/connectors/install` answers 410, and
`Client.InstallManagedPlugin` is gone (CERB-GAP-849).

**Every load checks the bundle.** The digest covers every file in the
directory, not only the entrypoint (`pluginhost.BundleDigest`), and a bundle with
a symlink is refused. `Manager.Load` runs `CheckBundle` before anything starts:
the host contract range must include this Cerberus, and a reviewed plugin's
bundle must match its accepted digest. A mismatch is `plugin_changed` (409),
whose text names `managed load <id> --accept-changes` as the recovery. A reload
of a changed bundle is refused before the running plugin is stopped. On a
terminal, `--accept-changes` shows the summary with a diff against the accepted
review (`pluginhost.Diff`) and re-accepts on the typed id, committing the bundle
under its new digest. Reinstalling an installed plugin is an upgrade, shown as
a diff, and nothing new reaches the daemon until it is accepted. Uninstall now
also removes the plugin's store copies, never a directory the operator owns.

**The state file changed shape and gained a lock.** `plugin-connectors.json`
is version 2: each entry has `id`, `plugin_dir` (where it runs from), `source`,
`options`, `loaded`, `bundle_digest`, the accepted `review` and `accepted_at`.
The CLI and the daemon both write it now, so every write is a read-modify-write
under an flock on `plugin-connectors.json.lock`, followed by a rename. A version 1
entry (`{plugin_dir, options, loaded}`) still loads, as `review_pending`: it
runs from the directory it was installed from, unchecked, and its previews are
not accepted. `managed review <id>` gives it the whole review once and moves it
into the store. `managed list` reports `review_pending`, `bundle_digest`,
`source` and `accepted_at`.

**Development installs.** `--dev` stays a devmode-build feature
(`docs/plugins.md`). A dev install is reviewed in place, runs from its source
directory, and is digest-checked on every load like any other, so a rebuild
needs `--accept-changes`. The one-shot `connectors plugin exec|health <dir>` is
unchanged: an unreviewed run in the operator's own process (CERB-GAP-846).

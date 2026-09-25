---
id: "CERB-CAP-303"
class: "capability"
name: "Daemon-managed plugin lifecycle"
summary: "Installs, loads, unloads and uninstalls plugins inside the daemon over the Cerberus socket, persisting the inventory so plugins come back after a restart."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "list/load/health/exec run live against the daemon at audit time; install, state shape and web routes re-read on main after P0 (#48 to #54)"
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
    note: "install by path still crosses the socket"
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
and, crucially, *kept* in `unrestored` so `persist()` does not quietly drop the
operator's registration; a rebuilt plugin comes back on the next restart. The
same path skips a plugin whose id has since become reserved.

Two consequences of "the daemon holds the process" are worth stating plainly.
First, the subprocess is unsupervised. `RPCProcess` notices its own exit and
fails pending calls, but the manager only deletes from `m.running` in `Unload`,
so a crashed plugin remains `loaded: true` in `managed list` and returns a
transport error from every operation until someone unloads it by hand. Nothing
restarts it. Second, the inventory is not a version record. There is no pin, no
recorded version, and no comparison against the hash computed at install. Since
PR #51 `managed list` reports that hash as `entrypoint_sha256`, but it is not
persisted and nothing compares it, so a replaced binary is visible to someone
comparing two listings and detected by nothing (CERB-GAP-336).

Surface coverage is uneven and the uneven part is the destructive part. CLI has
all seven verbs. The socket has all seven, and install is the one that still
takes a path (CERB-GAP-849). The web console (`web/src/pages/plugins.tsx`) had
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
unwritable log refuses them (CERB-CAP-604). A managed exec on the direct route
is recorded with the plugin's config and entrypoint fingerprints; the admin
lane's call into a plugin is recorded once, by the admin lane.

Since P1-4b the outcome also carries what the plugin reported for that call:
events it returned under `cerberus_telemetry`, which the host strips from the
result, and the stderr lines it wrote while the call ran. Both are bounded and
redacted by the plugin's own value redactor. A plugin can enrich the host's
record; it cannot write one.

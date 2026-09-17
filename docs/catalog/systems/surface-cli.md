---
id: "CERB-CAP-400"
class: "capability"
name: "CLI surface"
summary: "A 28-command cobra tree that is the only Cerberus surface covering every capability, mostly routed to the daemon socket but with three deliberate local-resolution exceptions."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.92
confidence_label: "Command tree enumerated from the installed binary; routing read from source at the matching commit"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "cmd/cerberus/"
tags:
  - "cerberus"
  - "surface"
  - "cli"
  - "cobra"
  - "area:surfaces"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "the CLI's column in the surface matrix"
  - type: "depends_on"
    target: "CERB-CAP-401"
    note: "most commands are socket clients"
  - type: "blocks"
    target: "CERB-GAP-436"
    note: "no CLI command for the namecheap record-set operations"
  - type: "blocks"
    target: "CERB-GAP-437"
    note: "no generic connector exec for built-ins"
  - type: "blocks"
    target: "CERB-GAP-448"
    note: "resource show bypasses the daemon"
---

# CLI surface

`cerberus` is a cobra command tree with 26 top-level commands, and it is the
only surface that covers every capability Cerberus has. Verified against the
installed binary at `/Users/cburks/go/bin/cerberus`, whose `--version` reports
`dev (commit 4ab19d0, built 2026-09-17T13:59:11Z)` — a commit that is an
ancestor of the audited `HEAD`, differing only in `AGENTS.md`. So for this area
source and binary agree, and a claim verified against one holds for the other.

There is no `cerberus version` subcommand, but `cerberus --version` / `-v`
exists and is stamped via `-ldflags` in the Makefile.

The tree is organised into three cobra groups plus ungrouped extras:

- **V2 Resource Commands** — `resource` (13 subcommands), `project`, `pipeline`,
  `register`, `deregister`, `registry`.
- **Daemon And Runtime Commands** — `config`, `daemon`, `init`, `install`,
  `uninstall`, `mcp`, `path`, `validate`, `web`.
- **Platform And Connector Commands** — `cloudflare`, `connectors`, `dns`,
  `docker`, `domain`, `forge`, `github`, `server`, `ssh`.
- **Ungrouped** — `mcp-http`, `run-secrets`, `completion`, `help`.

Most commands route to the daemon over the unix socket. Three notable
exceptions do not, and the difference matters:

- `resource show` resolves the config locally through
  `registry.ResolveConfig(cfgPath)` and never asks the daemon, so it reports the
  calling shell's view of the registry rather than the running daemon's.
  `resource list` asks the daemon. The two can disagree.
- `connectors` / `connectors describe` merge a local definition list with the
  daemon's, and deliberately take **liveness from the daemon** — the comment in
  `connectorDefinitions` says why: this process has the operator's shell `PATH`
  and credentials, the daemon has launchd's, and reporting the CLI's view is how
  a connector reads `LIVE=yes` while every call against it fails.
- `connectors plugin exec` / `plugin health` (without `managed`) install, load
  and run a plugin **in the calling process**, bypassing the daemon's plugin
  host entirely. `connectors plugin managed …` is the daemon-hosted path.

The CLI is the only surface for daemon lifecycle (`daemon start|stop|restart|
status`), launch-agent management (`install`, `uninstall`), layout introspection
(`path`), secret-reference expansion (`run-secrets`), config creation (`init`)
and plugin scaffolding (`connectors write-plugin-prototype`). Those are
genuinely local-process concerns and their absence elsewhere is not a gap.

What *is* a gap is that the CLI has no generic escape hatch for built-in
connector operations. `connectors plugin managed exec <id> <op>` runs any
operation on a *plugin* connector; there is no equivalent for `ssh`, `docker`,
`github`, `forge`, `cloudflare`, `namecheap` or `digitalocean`. Every built-in
operation reachable from the CLI is reachable only through a hand-written
subcommand, which is why `namecheap`'s `get_dns_record_set` and
`set_dns_record_set` — the safe whole-zone replacement that `cerberus dns list`'s
own help text tells the operator to use — have no CLI command at all.


---
id: "CERB-CAP-400"
class: "capability"
name: "CLI surface"
summary: "A 28-command cobra tree that is the only Cerberus surface covering every capability, mostly routed to the daemon socket but with a handful of deliberate in-process exceptions."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.92
confidence_label: "Command tree enumerated from the installed binary at audit time; in-process routing re-read on main after P0 (#48 to #54)"
last_reviewed: "2026-09-25"
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
  - type: "relates_to"
    target: "CERB-GAP-436"
    note: "the namecheap record-set operations, reachable through connectors exec"
  - type: "relates_to"
    target: "CERB-GAP-437"
    note: "connectors exec, the generic connector verb"
  - type: "blocks"
    target: "CERB-GAP-448"
    note: "resource show bypasses the daemon"
  - type: "relates_to"
    target: "CERB-GAP-846"
    note: "the in-process exceptions keep free-form power"
  - type: "blocks"
    target: "CERB-GAP-852"
    note: "refusals print the usage block first"
---

# CLI surface

`cerberus` is a cobra command tree with 26 top-level commands, and it is the
only surface that covers every capability Cerberus has. Verified against the
installed binary at `~/go/bin/cerberus`, whose `--version` reports
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

Most commands route to the daemon over the unix socket. These exceptions do
not, and the difference matters:

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
  host entirely. `connectors plugin managed …` is the daemon-hosted path. Since
  PR #50 nothing else can run a plugin directory: the socket and web routes for
  it answer 410.
- `cerberus docker` with `--host`, `--context` or `-f` runs **in-process**,
  because since PR #52 the socket refuses ad-hoc docker targets
  (CERB-DEC-813).
- An explicit `--config` forces connector verbs in-process (PR #50), so a
  resource id is never resolved against a config other than the one the
  operator named.

Those in-process paths keep free-form power that no remote surface has, which
is why in-process is not a trust boundary (CERB-GAP-846).

The CLI is the only surface for daemon lifecycle (`daemon start|stop|restart|
status`), launch-agent management (`install`, `uninstall`), layout introspection
(`path`), secret-reference expansion (`run-secrets`), config creation (`init`)
and plugin scaffolding (`connectors write-plugin-prototype`). Those are
genuinely local-process concerns and their absence elsewhere is not a gap.

The generic escape hatch for connector operations is `connectors exec <id>
<op>` (CERB-GAP-437, closed). It runs any operation on any connector, built-in
or loaded plugin, through the admin lane (`ExternalConnectorService.Execute`),
so dry-run, acknowledgment and redaction apply exactly as they do for a
hand-written subcommand. Arguments are typed from the operation's input schema:
`--arg k=v` is parsed as the declared integer, number or boolean, a repeated key
builds an array, `--arg-json k=<JSON>` carries objects, and `--input <file|->`
takes a whole argument object. What the CLI knows about the operation (an
unknown connector or operation, an argument a closed schema does not declare, a
value that does not fit its type) is printed as a hint, and the call is sent
anyway, so the admin lane refuses it and records the attempt in the audit log.
Only a command line that cannot form a request, such as `--arg` without `=`, is
refused locally. `connectors plugin managed exec`, which had become the same
command limited to installed plugins, was retired on 2026-09-25 with no
tombstone. The one-shot `connectors plugin exec <dir>` types its arguments from
the directory's `plugin.yaml` and follows the same hint-and-send rule. That is
also the CLI path to `namecheap`'s `get_dns_record_set` and `set_dns_record_set`
(CERB-GAP-436), with the authoritative records passed as `--arg-json records=…`
or `--input`. A generic verb is still a worse operator experience than a typed
subcommand with real flags, which is why the everyday verbs keep theirs.

## Since P1-5

`connectors plugin managed install <dir>`, `managed review <id>` and
`managed load <id> --accept-changes` run the install review in the CLI process
and only from an interactive terminal: stdin and stdout must both be a TTY, so
`</dev/null` is refused. They confirm by the typed plugin id, then ask the
daemon to reload the plugin by id, or say the daemon picks it up at start when it
is not running. The one-shot `plugin exec|health <dir>` is unchanged and
unreviewed.

## Since P2-1

The root command classifies its own process before any subcommand runs
(`PersistentPreRun` in `main.go`, `cerbapi.DetectCLI`). It is human only from
an interactive terminal with no `CERBERUS_PRINCIPAL=agent`, and an agent
otherwise. The claim rides on every socket request and on every in-process
call's audit record. `cerberus whoami` prints the classification and the inputs
that decided it. When the daemon is up, it also prints the principal the daemon
gives the same caller, with its verified uid (CERB-TOOL-419). Agent launchers
set the marker (Decision 19), and `whoami` is how the operator checks that one
did.

`cerberus policy` (P2-4) is the operator's view of the policy decision point:
- `explain <connector.op>` prints the decision and every matched rule, for a
  `--target`, a principal kind (`--as`, default this terminal's
  classification), a dry run or an ad hoc target. It evaluates the applied
  snapshot, or the working files with `--working`, and shows the declared
  posture.
- `apply` runs only on a TTY. It samples every declared operation against
  every registered resource of its connector, plus an unregistered and an ad
  hoc target, for each principal kind, and prints the decisions that flip.
  It writes `applied.yaml` and its hash only after the typed
  `apply <shorthash>`, recorded as an `admin` event.
- `report` groups the recorded `would_block` decisions from the audit log.

Policy is never changed over the socket, the web console or MCP.

## Since P3-1

`cerberus approvals list [--status …]` and `cerberus approvals show <id>` show
the approval requests (CERB-TOOL-870, CERB-TOOL-871). They ask the daemon, and
with the daemon down read the store directly and say so. Nothing here decides
an approval.

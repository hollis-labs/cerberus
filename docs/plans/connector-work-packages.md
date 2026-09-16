# Connector Work Packages

Self-contained briefs for execution agents extending the admin lane. Read
`docs/plans/infra-admin-control-plane.md` first for the direction and the
constraints; this file is the work breakdown.

Each package names its own files, its shared files, its acceptance criteria, and
what it must not do. Take one package. Do not take two.

## How a connector verb is actually added

Discovered the hard way while shipping `ssh put`/`get`. Budget **five** touch
points, not one:

| # | File | What goes there |
|---|---|---|
| 1 | `internal/connector/<x>/connector.go` | The `contract.Operation` entry: name, description, `InputSchema`, `Destructive`, `SupportsDry`, examples |
| 2 | `internal/cerbapi/external_connector_service.go` | A `case` in `execute<X>`, and a `case` in `dryRunPreview` if `SupportsDry` |
| 3 | `internal/cerbapi/connector_payload.go` | A `decodeConnectorPayload` case **if the operation returns a typed DTO** |
| 4 | `cmd/cerberus/cmd_<x>.go` | The cobra command |
| 5 | `internal/mcp/tools_<x>.go` **plus** `cmd_mcp.go` **and** `cmd_daemon.go` | The MCP tool and its two registrations |

Three traps, each of which cost real time:

- **Skipping #3 gives `unexpected result type json.RawMessage`.** The operation
  works; the CLI cannot type the result coming back over the daemon socket. Only
  needed for typed DTOs — `string` payloads already have a case.
- **Skipping the `dryRunPreview` case makes `--dry-run` demand `--ack`.** The
  dry-run early-return only fires when `dryRunPreview` returns `ok=true`;
  otherwise it falls through to the acknowledgment gate.
- **MCP tools are not generated for built-in connectors.** Only plugin
  connectors get `pluginhost.ToolNameForOperation`. Built-ins are hand-written
  and registered twice — missing `cmd_daemon.go` means the tool works under
  `cerberus mcp` and not under the daemon.

### House rules

- **Destructive means destructive.** Anything that writes, replaces, deletes or
  reboots gets `Destructive: true`, and `SupportsDry: true` if a preview is
  meaningful. Read-only operations get neither — making reads prompt empties the
  gate of meaning.
- **Never log or return a secret value.** Follow the `probe-*` convention from
  `~/Projects/tools`: environment variable *names*, never values. Error paths go
  through `redact.Text`.
- **Credentials come from the secret provider**, never from a config field. See
  `docs/secrets.md` and copy how `digitalocean.New` does it.
- **Put the vendor SDK behind a `Backend` interface** in the connector package,
  the way `digitalocean` and `docker` already do. This is what makes a v0.x
  dependency swappable and the connector testable without network.
- Tests use a fake `Backend`. See `fakeSSHBackend` in
  `internal/cerbapi/external_connector_service_test.go`.
- `make test` and `golangci-lint run --new-from-rev=main ./...` must both be
  clean. The repo has ~149 pre-existing lint issues; `--new-from-rev` is the
  gate that matters. Do not "fix" the pre-existing ones in your branch.

### Verifying against real systems

muctlvaig is a corporate host on VPN and ContextForge is live. Probe with
read-only operations, use `--dry-run` before any write, and clean up test
artifacts. Do not restart, reconfigure or deploy anything on that host. If a
package needs a real write to prove itself, write to a scratch path under
`/home/cburks/` and remove it afterwards.

## Sequencing

```
WP-1 (dry-run extraction)  ──►  WP-2 (remote docker)   ─┐
                                WP-3 (ContextForge)    ─┼──► parallel
                                WP-4 (Azure)           ─┘
WP-5 (validation)  — independent, needs a decision first
```

**WP-1 lands first.** It rewrites the 600-line `dryRunPreview` switch, which
every other package also touches. Running it concurrently with WP-2/3/4
guarantees conflicts in the worst possible file.

After WP-1, the remaining packages own disjoint connector directories and touch
the shared files only in append-style `case` arms, which merge cleanly.

---

## WP-1 — Extract per-connector dry-run previews

**Why:** `dryRunPreview` in `external_connector_service.go` is a single ~600-line
switch (lines ~258–865) covering every connector. It is the largest merge
conflict surface in the repo and the reason adding a connector feels invasive.

**Do:** turn it into a dispatch table. Move each connector's preview cases into a
function beside that connector's `execute<X>` — either in
`external_connector_service.go` as `dryRunPreview<X>(args)` or, preferably, into
a new `internal/cerbapi/dryrun_<x>.go` per connector. `dryRunPreview` becomes a
lookup on `args.Connector`.

**Constraint: behaviour must not change.** This is a pure refactor. Every
existing preview must produce byte-identical output. Do not "improve" a summary
string, reorder a target map, or add a warning while moving it.

**Files:** `internal/cerbapi/external_connector_service.go`, new
`internal/cerbapi/dryrun_*.go`.

**Acceptance:**
- `make test` clean, `--new-from-rev=main` clean.
- `cerberus ssh put muctlvaig <file> /home/cburks/wp1-probe.txt --dry-run`
  produces the same JSON as before the change (capture it first).
- A spot check of one other connector's dry run, e.g.
  `cerberus cloudflare dns create ... --dry-run`, unchanged.
- Adding a new connector now means one new file, not an edit inside a 600-line
  switch. Say so in the commit message.

**Do not:** change any operation's `Destructive`/`SupportsDry` flags, or touch
the `Execute` dispatch switch. Dry run only.

---

## WP-2 — Remote Docker over SSH

**Why:** the same Docker operations should target a remote daemon, so one
implementation serves both the Azure box and muctlvaig. This is the cheapest
path to remote container administration — no new connector, no new SDK.

**Do:** add host selection to the Docker connector. `DOCKER_HOST` (including
`ssh://user@host`) and/or `docker context` selection, configurable per
operation rather than per process, so one daemon can talk to several hosts.

**Design notes:**
- The CLI backend shells out to `docker`, which already understands
  `DOCKER_HOST=ssh://`. Setting it on the `exec.Cmd` environment is likely the
  whole feature. Confirm before building anything larger.
- `DetectDocker()` and the `CERBERUS_DOCKER_PATH` override already landed; do
  not re-litigate binary discovery.
- Surface the target host in errors. "connection refused" with no host named is
  the failure mode to avoid.

**Known constraint — verify, do not assume:** `cburks` is **not** in the
`docker` group on muctlvaig. Confirmed 2026-09-16:

```
$ cerberus ssh exec muctlvaig --ack -- 'id; docker ps'
uid=12989(cburks) gid=11000(hsv-all) groups=11000(hsv-all),20922(muctlvaig)
permission denied while trying to connect to the docker API at unix:///var/run/docker.sock
```

So **muctlvaig cannot be your acceptance test for the happy path.** Use Docker
Desktop locally over `ssh://localhost` if key auth to localhost is available, or
document what could not be verified and why. A clear "blocked, here is the
evidence" is a better result than a test that quietly proves nothing.

**Files:** `internal/connector/docker/`, `cmd/cerberus/cmd_docker.go`,
`internal/mcp/tools_docker.go`, plus the shared touch points.

**Acceptance:** an operation runs against a non-default Docker host and the
result is demonstrably from that host, not the local daemon. Permission failures
name the host and suggest the `docker` group.

---

## WP-3 — ContextForge connector

**Why:** ContextForge is the MCP gateway the Adtran agent platform runs on, and
every administrative change to it is currently a shell script or the admin UI.

**Library:** `github.com/leefowlercu/go-contextforge` v0.9.0. Pin it. Community,
not IBM — behind a `Backend` interface, non-negotiable.

**Operations, in priority order.** Read-only first; they are safe, they prove
auth and connectivity, and they are what agents will call most:

1. `list_gateways` — upstream MCP server registrations
2. `list_virtual_servers` — the composed catalogs
3. `list_tools` — gateway-prefixed tool names
4. `get_health` — `/health` is open, everything else 401s

Then writes, each `Destructive` + `SupportsDry`: register/update a gateway,
compose a virtual server, toggle a tool.

**What our own probes already established** — do not rediscover:
- CF accepts `Authorization: Bearer <JWT>` **only**. `X-API-Key` and a raw token
  both 401.
- The admin UI renames things: 🖥️ "MCP Servers" is `tab-gateways`, the upstream
  registrations and **the only place auth can be set**. 🔗 "Virtual Servers" is
  the catalog and has no auth fields.
- Tool names are gateway-prefixed (`mcp-workday-*`) and change if a virtual
  server is renamed. Read them from `/api/tools`; never guess.
- There is a root `/mcp` aggregating every tool across all gateways. Useful for
  testing a gateway before a virtual server exists. **Do not register it in a
  client** — it imports everything.

**Reachability:** CF is at `127.0.0.1:14444` through the `tunnel-muctlvaig`
resource, or on the box. Base URL must be configurable. If the tunnel is down
the connector must say "tunnel is down", not "gateway is down" — a refused
connection on 14444 means the former. Check
`cerberus resource status tunnel-muctlvaig` in the error path or name it in the
message.

**Secret:** JWT via the secret provider, `keychain://`. Never in config.

**Files:** new `internal/connector/contextforge/`, new
`cmd/cerberus/cmd_contextforge.go`, new `internal/mcp/tools_contextforge.go`,
registration in `internal/app/app.go` (use `RegisterFactory`, like every
credentialed connector), plus the shared touch points.

**Acceptance:** `cerberus contextforge gateways` lists the real upstream
registrations through the tunnel. Read-only operations verified live; writes
verified by `--dry-run` only unless explicitly cleared to write.

---

## WP-4 — Azure connector

**Why:** an Azure dev box is being provisioned for development, testing and
experiments. The main prize is start/stop — a deallocated Azure VM stops billing
compute, so this is cost control, not convenience.

**Shape:** copy `internal/connector/digitalocean/` almost exactly. It is the
agreed model: `Backend` interface, SDK behind it, secret provider, typed
operations, `resource.Server` type.

**Library:** `github.com/Azure/azure-sdk-for-go` — `armcompute` for VMs.

**Operations:** `list_vms`, `get_vm`, `start_vm`, `deallocate_vm` (destructive),
`create_vm` (destructive + dry-run), `delete_vm` (destructive + dry-run).

**Note the Azure-specific distinction** DigitalOcean does not have: *stopped* and
*deallocated* are different states and only deallocated stops compute billing.
`stop_vm` that leaves the VM allocated is a trap — name the operations so the
billing-relevant one is the obvious choice, and say so in the descriptions.

**Auth:** `azidentity`. The `az` CLI is installed and configured locally, so
`AzureCLICredential` is the path of least resistance for a first cut;
`DefaultAzureCredential` chains it. Do not build a credential story beyond what
the secret provider and `azidentity` already give you.

**Blocked on:** the box being provisioned. The connector can be built and
unit-tested against a fake `Backend` before then — do that, and mark live
verification as pending rather than faking a result.

**Capture for provisioning day:** add the operator's user to the `docker` group
on that box. It is greenfield, so this is free there, and it is what makes WP-2
work without sudo — unlike muctlvaig.

**Files:** new `internal/connector/azure/`, new `cmd/cerberus/cmd_azure.go`, new
`internal/mcp/tools_azure.go`, registration in `internal/app/app.go`, plus the
shared touch points.

---

## WP-5 — Reject container resources at validation

**Status: needs a decision before starting.**

A `type: container` / `connector: docker` resource passes `cerberus validate`
today and then fails on every runtime operation, because validation only checks
that `type` and `connector` are non-empty while
`resource_runtime_service.go` accepts local/process only.

Verified:

```
$ cerberus validate probe.cerberus.yaml     # type: container, connector: docker
probe.cerberus.yaml: OK
```

Two options:

- **Reject** in registry validation with a message pointing at the connector
  operations. Leaning this way: a clean validation that cannot run is the worst
  of both worlds, and it will mislead exactly the newcomers the app is about to
  be shared with.
- **Warn** — more permissive, but leaves the trap in place.

Whichever is chosen, the message must name the alternative: container
administration goes through `cerberus docker ...`, not through a resource.

**Do not start this package until the option is chosen.**

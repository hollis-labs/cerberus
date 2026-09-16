# Connector Work Packages

Self-contained briefs for execution agents extending the admin lane. Read
`docs/plans/infra-admin-control-plane.md` first for the direction and the
constraints; this file is the work breakdown.

Each package names its own files, its acceptance criteria, and what it must not
do. Take one package. Do not take two.

## Core or plugin

Decided 2026-09-16.

**Core, compiled into the binary** — primitives the control plane is built on,
that other connectors depend on, carrying no vendor SDK. Cerberus ships as a
single installed binary and these are part of it.

**Plugin, standalone** — provider integrations that are optional per user, carry
a third-party SDK, and ship on someone else's schedule. Loaded at runtime with
no rebuild of the host.

| Connector | Today | Goes | Why | Vendor SDK |
|---|---|---|---|---|
| `local` | compiled | **Core** | It *is* the supervision lane (process/launchd), not an external connector | none |
| `ssh` | compiled | **Core** | Primitive: remote docker rides it, deploys and file transfer are built on it | `x/crypto`, `pkg/sftp` (already core) |
| `docker` | compiled | **Core** | Primitive: shells out to the CLI, remote docker rides ssh | none |
| `github` | compiled | **Core** | Cerberus's own release and pipeline story leans on it | `go-github` (7MB) |
| `cloudflare` | compiled | **Plugin** | DNS provider, optional; the largest dependency win available | `cloudflare-go/v4` (33MB) |
| `digitalocean` | compiled | **Plugin** | VPS provider, optional | `godo` (2.7MB) |
| `forge` | compiled | **Plugin** | Laravel Forge, niche | none |
| `namecheap` | compiled | **Plugin** | Registrar, optional | none |
| **ContextForge** | — | **Plugin** | Adtran-specific; a v0.x SDK against an evolving gateway, so rebuild-independence pays most | `go-contextforge` |
| **Azure** | — | **Plugin** | Vendor SDK, optional | `azure-sdk-for-go` |

**Our plugins live in `hollis-labs/cerberus-plugins`**, one directory per plugin,
shared CI and release. Separate repos buy independent tagging we do not need
yet; three repos for two plugins is overhead.

**Do not migrate the four existing connectors yet.** They work today, and moving
them is cost with no feature benefit — and it would be migrating onto a lane
that has never carried a plugin authored as a plugin from day one. Build
ContextForge first, learn what authoring actually feels like, then migrate
starting with `cloudflare` where the 33MB payoff is. Forge and Namecheap have no
SDK to shed, so they are last.

For reference, the binary is currently ~81MB.

## How a connector verb is actually added

Discovered the hard way while shipping `ssh put`/`get`. Budget **five** touch
points for a core connector, not one:

| # | File | What goes there |
|---|---|---|
| 1 | `internal/connector/<x>/connector.go` | The `contract.Operation`: name, description, `InputSchema`, `Destructive`, `SupportsDry`, examples |
| 2 | `internal/cerbapi/external_connector_service.go` | A `case` in `execute<X>`, and a `case` in `dryRunPreview` if `SupportsDry` |
| 3 | `internal/cerbapi/connector_payload.go` | A `decodeConnectorPayload` case **if the operation returns a typed DTO** |
| 4 | `cmd/cerberus/cmd_<x>.go` | The cobra command |
| 5 | `internal/mcp/tools_<x>.go` **plus** `cmd_mcp.go` **and** `cmd_daemon.go` | The MCP tool and its two registrations |

Three traps, each of which cost real time:

- **Skipping #3 gives `unexpected result type json.RawMessage`.** The operation
  works; the CLI cannot type the result coming back over the daemon socket. Only
  needed for typed DTOs — `string` payloads already have a case.
- **Skipping the `dryRunPreview` case makes `--dry-run` demand `--ack`.** The
  dry-run early return only fires when `dryRunPreview` returns `ok=true`;
  otherwise it falls through to the acknowledgment gate.
- **MCP tools are not generated for built-in connectors.** Only plugin
  connectors get `ToolNameForOperation`. Built-ins are hand-written and
  registered twice — miss `cmd_daemon.go` and the tool works under
  `cerberus mcp` but not under the daemon.

A plugin connector is different: it declares its operations in its manifest and
the host derives tool names, so a plugin does not touch any of the five.

### House rules

- **Destructive means destructive.** Anything that writes, replaces, deletes or
  reboots gets `Destructive: true`, and `SupportsDry: true` if a preview is
  meaningful. Read-only operations get neither — making reads prompt empties the
  gate of meaning.
- **Never log or return a secret value.** Follow the `probe-*` convention from
  `~/Projects/tools`: environment variable *names*, never values. Error paths go
  through `redact.Text`.
- **Credentials come from the secret provider**, never a config field. See
  `docs/secrets.md`; copy how `digitalocean.New` does it.
- **Put the vendor SDK behind a `Backend` interface** in the connector package,
  as `digitalocean` and `docker` already do. This is what makes a v0.x
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
WP-0 plugin gaps          DONE
WP-1 dry-run extraction   ──► WP-3 remote docker
WP-2 pkg/plugin promotion ──► WP-4 ContextForge ──► WP-5 Azure
WP-6 container validation     (independent; needs a decision)
```

WP-1 and WP-2 are independent of each other and both unblock work downstream.
WP-1 rewrites the 600-line `dryRunPreview` switch, so nothing else that touches
that file should run beside it.

---

## WP-0 — Plugin lane gaps — DONE 2026-09-16

Three gaps that made the plugin lane unsafe to hand to anyone:

- An installed-but-unloaded plugin **disabled the connector it shadowed**.
  `Execute` now falls back to the built-in and only errors when nothing can
  serve the id.
- Installing a local plugin required falsely claiming it was signed. Added
  `TrustModeLocal`: unsigned local installs need no flags and record
  `TrustTierUnsigned`. The host computes the entrypoint hash itself.
- Added `cerberus connectors plugin managed uninstall`.

Note one policy call: `TrustTierUnsigned` groups with the trusted tiers in
`OperationAllowed`, not the dev tiers. A signature attests to provenance; the
acknowledgment gate attests to intent. Grouping it with dev would block every
destructive operation and leave a read-only plugin lane. Destructive operations
still require `--ack`.

---

## WP-1 — Extract per-connector dry-run previews

**Why:** `dryRunPreview` in `external_connector_service.go` is a single ~600-line
switch (lines ~258–865) covering every connector. It is the largest merge
conflict surface in the repo and the reason adding a connector feels invasive.

**Do:** turn it into a dispatch table. Move each connector's preview cases into a
function beside that connector's `execute<X>` — preferably a new
`internal/cerbapi/dryrun_<x>.go` per connector. `dryRunPreview` becomes a lookup
on `args.Connector`.

**Constraint: behaviour must not change.** This is a pure refactor. Every
existing preview must produce byte-identical output. Do not improve a summary
string, reorder a target map, or add a warning while moving it.

**Acceptance:**
- `make test` clean, `--new-from-rev=main` clean.
- Capture `cerberus ssh put muctlvaig <file> /home/cburks/wp1-probe.txt --dry-run`
  before the change; identical JSON after.
- Spot check one more, e.g. a `cloudflare dns create ... --dry-run`.
- Adding a connector now means one new file, not an edit inside a 600-line
  switch. Say so in the commit message.

**Do not:** change any operation's `Destructive`/`SupportsDry` flags, or touch
the `Execute` dispatch switch.

---

## WP-2 — Promote the plugin authoring contract to `pkg/plugin`

**Why this blocks everything plugin-shaped:** `internal/plugins/dockerplugin`
imports `github.com/chrispian/cerberus/internal/pluginhost`. An external module
cannot import `internal/`, so **a plugin in `hollis-labs/cerberus-plugins` will
not compile today.**

**Do:** move the *authoring* surface to a public package, keeping the *host*
surface internal. The boundary is: a plugin author needs the manifest and
tool-naming contract; they do not need the host machinery.

Promote to `pkg/plugin`:

- `ToolNameForOperation` and `OperationFromToolName` (`sdk_protocol.go`)
- `PluginYAML`, `Entrypoint`, `PluginYAMLFromManifest`, `PluginYAMLFilename`
  (`plugin_yaml.go`)

Keep in `internal/pluginhost`: `Manager`, `DirectoryInstaller`, `TrustPolicy`,
`SubprocessLauncher`, `StdioTransportFactory`, the `SDK*` protocol types, and
everything else on the host side.

`pkg/connector` already carries `Manifest` and `ManifestFromDefinition`, so the
contract half is done — this is the remaining gap.

**Known constraint — do not try to solve this, just work around it.** The module
path in `go.mod` is `github.com/chrispian/cerberus`, the remote is
`hollis-labs/cerberus`, and the declared path does not resolve publicly:

```
$ go list -m github.com/chrispian/cerberus@latest
ERROR: Repository not found.
```

So an external module cannot `go get` these packages. For the acceptance test
below, use a `replace` directive pointing at the local checkout. Whether to
rename the module path, publish at the declared path, or keep `replace`
directives in the plugin repo is an owner decision that is **out of scope for
this package** — flag it, do not change `go.mod`'s module line.

**Acceptance:**
- `internal/pluginhost` keeps working, re-exporting or importing from
  `pkg/plugin` as needed; no behaviour change.
- A scratch module outside this repo, with a `replace` pointing at this
  checkout, importing only `pkg/connector`, `pkg/resource`, `pkg/plugin` and
  `plugin-sdk/subprocess`, compiles a trivial plugin. Prove this — it is the
  whole point of the package. Report the exact `go.mod` that worked, since the
  plugin repo will need the same shape.
- `internal/plugins/dockerplugin` builds against the public packages and the
  generated prototype still installs, loads and serves `docker ps`.

**Do not:** move the trust policy or the subprocess launcher. Those are host
decisions and must not be something a plugin can influence.

---

## WP-3 — Remote Docker over SSH *(core connector)*

**Why:** the same Docker operations should target a remote daemon, so one
implementation serves both the Azure box and muctlvaig. No new connector, no new
SDK.

**Do:** add host selection to the Docker connector — `DOCKER_HOST` (including
`ssh://user@host`) and/or `docker context`, configurable per operation rather
than per process, so one daemon can talk to several hosts.

**Design notes:**
- The CLI backend shells out to `docker`, which already understands
  `DOCKER_HOST=ssh://`. Setting it on the `exec.Cmd` environment is likely the
  whole feature. Confirm before building anything larger.
- `DetectDocker()` and `CERBERUS_DOCKER_PATH` already landed; do not re-litigate
  binary discovery.
- Surface the target host in errors. "connection refused" with no host named is
  the failure mode to avoid.

**Known constraint — verify, do not assume:** `cburks` is not in the `docker`
group on muctlvaig. Confirmed 2026-09-16:

```
$ cerberus ssh exec muctlvaig --ack -- 'id; docker ps'
uid=12989(cburks) gid=11000(hsv-all) groups=11000(hsv-all),20922(muctlvaig)
permission denied while trying to connect to the docker API at unix:///var/run/docker.sock
```

So **muctlvaig cannot be the happy-path test.** Use Docker Desktop locally over
`ssh://localhost` if key auth to localhost is available, or document what could
not be verified and why. A clear "blocked, here is the evidence" beats a test
that quietly proves nothing.

**Acceptance:** an operation runs against a non-default Docker host and the
result is demonstrably from that host. Permission failures name the host and
suggest the `docker` group.

---

## WP-4 — ContextForge plugin *(first real plugin)*

**Depends on WP-2.**

**Why a plugin:** Adtran-specific, so it does not belong in everyone's binary,
and it tracks a v0.x community SDK against an evolving gateway — the case where
rebuild-independence pays most. It also has no built-in with the same id, so it
cannot hit the shadowing class of bug WP-0 fixed.

**Where:** `hollis-labs/cerberus-plugins`, directory `contextforge/`. This is the
first plugin in that repo, so it also establishes the layout, CI and release
shape. Use `internal/plugins/dockerplugin` + `cmd/cerberus-docker-plugin` in this
repo as the reference implementation.

**Library:** `github.com/leefowlercu/go-contextforge` v0.9.0, pinned, behind a
`Backend` interface. Non-negotiable — it is what makes a v0.x dependency
swappable.

**Operations, in priority order.** Read-only first: safe, they prove auth and
connectivity, and they are what agents call most.

1. `list_gateways` — upstream MCP server registrations
2. `list_virtual_servers` — the composed catalogs
3. `list_tools` — gateway-prefixed tool names
4. `get_health` — `/health` is open, everything else 401s

Then writes, each `Destructive` + `SupportsDry`: register/update a gateway,
compose a virtual server, toggle a tool.

**What our probes already established** — do not rediscover:

- CF accepts `Authorization: Bearer <JWT>` **only**. `X-API-Key` and a raw token
  both 401.
- The admin UI renames things: 🖥️ "MCP Servers" is `tab-gateways`, the upstream
  registrations and **the only place auth can be set**. 🔗 "Virtual Servers" is
  the catalog and has no auth fields.
- Tool names are gateway-prefixed (`mcp-workday-*`) and change if a virtual
  server is renamed. Read them from `/api/tools`; never guess.
- There is a root `/mcp` aggregating every tool across all gateways — useful for
  testing a gateway before a virtual server exists, but **never register it in a
  client**, it imports everything.

**Reachability:** CF is at `127.0.0.1:14444` through the `tunnel-muctlvaig`
resource, or on the box. Base URL must be configurable. If the tunnel is down
the connector must say "tunnel is down", not "gateway is down" — a refused
connection on 14444 means the former.

**Secret:** JWT via the secret provider, `keychain://`. Never in config.

**Install:** unsigned local is now the supported path —
`cerberus connectors plugin managed install <dir>` with no trust flags, which
records `trust_tier: unsigned`.

**Acceptance:** the plugin installs unsigned, loads, and
`cerberus connectors plugin managed exec contextforge list_gateways` returns the
real upstream registrations through the tunnel. Writes verified by `--dry-run`
only unless explicitly cleared to write.

---

## WP-5 — Azure plugin

**Depends on WP-2. Live verification blocked on the box being provisioned.**

**Where:** `hollis-labs/cerberus-plugins`, directory `azure/`.

**Shape:** follow `internal/connector/digitalocean/` — `Backend` interface, SDK
behind it, secret provider, typed operations, `resource.Server` type — packaged
as a plugin the way WP-4 establishes.

**Library:** `github.com/Azure/azure-sdk-for-go`, `armcompute` for VMs.

**Operations:** `list_vms`, `get_vm`, `start_vm`, `deallocate_vm` (destructive),
`create_vm` (destructive + dry-run), `delete_vm` (destructive + dry-run).

**The Azure-specific distinction DigitalOcean does not have:** *stopped* and
*deallocated* are different states, and only deallocated stops compute billing.
A `stop_vm` that leaves the VM allocated is a trap. Name the operations so the
billing-relevant one is the obvious choice, and say so in the descriptions.

**Auth:** `azidentity`. The `az` CLI is installed and configured locally, so
`AzureCLICredential` is the path of least resistance for a first cut;
`DefaultAzureCredential` chains it. Do not build a credential story beyond what
the secret provider and `azidentity` already give you.

Build and unit-test against a fake `Backend` before the box exists, and mark
live verification as pending rather than faking a result.

**Capture for provisioning day:** add the operator's user to the `docker` group
on that box. It is greenfield, so this is free there, and it is what makes WP-3
work without sudo — unlike muctlvaig.

---

## WP-6 — Reject container resources at validation

**Status: needs a decision before starting.**

A `type: container` / `connector: docker` resource passes `cerberus validate`
and then fails on every runtime operation, because validation only checks that
`type` and `connector` are non-empty while `resource_runtime_service.go` accepts
local/process only.

```
$ cerberus validate probe.cerberus.yaml     # type: container, connector: docker
probe.cerberus.yaml: OK
```

**Reject** in registry validation with a message pointing at the connector
operations, or **warn**. Leaning reject: a clean validation that cannot run is
the worst of both worlds, and it will mislead exactly the newcomers the app is
about to be shared with. Either way the message must name the alternative —
container administration goes through `cerberus docker ...`, not a resource.

**Do not start until the option is chosen.**

# Cerberus

Cerberus is a single-binary Go control plane for the infrastructure we
administer. It builds, deploys, supervises and inspects that infrastructure
over one runtime service that the CLI, daemon socket, HTTP API, web console and
MCP adapter all share. Every capability added to a connector becomes a CLI
command, an API operation and an MCP tool at the same time — which is what
makes it usable by agents as well as by people.

It owns execution and derived operational state — not the definitions a project
writes about itself, the data it holds, or the credentials it needs.
Operationally it is v2-only: `resources:` are the model, the legacy `services:`
lane is frozen.

## This manages real systems

**Work on this repo changes what happens to production and corporate
infrastructure.** Read this section before running anything.

- **`muctlvaig.corp.adtran.com`** — Adtran work host, reachable only on VPN.
  Runs the ContextForge MCP gateway (`contextforge-gateway:4444` on the
  `agent-gateway-net` docker network), `workday-mcp`, `ess-agent-workday`,
  `ess-teams-bot` and the Nanite demo. `cburks` is **not** in the `docker`
  group there, so container introspection goes over HTTP, not `docker ps`.
- **Real Workday writes.** `workday-mcp` runs with `WORKDAY_ENABLE_WRITES=true`
  against the `adtran_preview` tenant. A time-off request from a local test
  writes for real and routes to a real approver.
- **Azure dev box** — pending provisioning, for development, testing and
  experiments. Docker will run there too.
- **Corporate SSH.** A Cerberus-started `ssh` has no TTY and cannot answer a
  password or MFA prompt. An auto-restarting tunnel against a corporate auth
  endpoint is a good way to get an account locked out, which is why the tunnel
  resources are deliberately on/off with `auto_start` and `auto_restart` both
  false.

Destructive connector operations require explicit operator acknowledgment
(`--ack`) and most support `--dry-run`. Use the preview first.

## Start Here

- `README.md` — install paths, CLI reference, port map.
- `docs/plans/infra-admin-control-plane.md` — the current direction: making
  Cerberus the single place we run ssh, file transfer, deploys and host
  administration. Read this before adding connector capability.
- `docs/adr/0002-resource-only-local-workload-model.md` — why v2 is the only
  model, and what "frozen" means for `services:`.
- `docs/adr/0003-connector-response-dtos.md` — why a connector operation returns
  a Cerberus DTO and never a vendor SDK type. Read before writing one.
- `internal/cerbapi/resource_runtime_service.go` — the shared runtime service
  for supervised local workloads. Behavior changes belong here, not in a caller.
- `internal/cerbapi/external_connector_service.go` — the imperative admin lane:
  per-call resolution, dry-run, acknowledgment, redaction, MCP tool generation.
  New administrative verbs belong here.
- `internal/domain/connector.go` — the interface every provider implements.
- `internal/connector/local/` — `dev_session.go` and `launchd.go` are the two
  runtime modes, `artifact.go` owns the build→install join.
- `pkg/plugin/` — the public plugin authoring contract: `plugin.yaml` and MCP
  tool naming. A plugin outside this repo imports this, `pkg/connector` and
  `pkg/resource`; the host half stays in `internal/pluginhost`.
- `internal/registry/` — discovery and validation of per-repo `*.cerberus.yaml`.
- `docs/secrets.md` — how a resource names a credential without carrying one.
- `~/Projects/tools` — ~70 shell scripts that already administer the work host,
  with their failure modes documented in-line. This is the capability spec for
  what connectors should grow; promote proven behaviour rather than redesigning.

## Commands

```bash
make test        # go test ./cmd/cerberus ./internal/... ./pkg/...
make build       # → bin/cerberus
make all         # web bundle into internal/webui/dist, then the binary
make lint        # go vet, golangci-lint, staticcheck, errcheck, govulncheck
make typecheck   # web/ TypeScript
```

Use `make all`, not `make build`, whenever `web/` changes: the console is
`go:embed`-ed from `internal/webui/dist`, which is gitignored, so a plain build
embeds whatever bundle is sitting there.

Lefthook is the gate; there is no CI. Pre-commit runs gofmt/goimports,
`golangci-lint --new` and `go vet` on staged Go, pre-push the full `go test`.

## Two lanes, and they are not interchangeable

**The supervision lane** (`resource_runtime_service.go`) is for long-lived local
workloads: `auto_restart`, health probes, launchd, artifact staleness. It is
hardcoded to local/process in roughly ten places, deliberately.

**The admin lane** (`external_connector_service.go`) is for imperative
administration: run a verb against a remote system, get a result. Stateless,
resolved per call.

Remote containers and remote hosts belong in the admin lane. Do not widen the
supervision lane to reach them — see
`docs/plans/infra-admin-control-plane.md`.

Note the trap: a `type: container` resource currently passes `cerberus validate`
and then fails on every runtime operation, because validation only checks that
`type` and `connector` are non-empty.

## Core or plugin

Cerberus ships as a single installed binary. Compiled in: `local`, `ssh`,
`docker` — the primitives the control plane is built on, none of which carries a
vendor SDK — plus `github`, which does carry one but earns its place because
Cerberus's own release and pipeline story leans on it. Everything else that
talks to a provider is a plugin: optional per user, its own release schedule,
loaded at runtime without rebuilding the host.

Our plugins live in `hollis-labs/cerberus-plugins`; third-party plugins are
standalone repos. `cloudflare`, `digitalocean`, `forge` and `namecheap` are
compiled in today and will migrate later — do not add a fifth.

**A new provider integration is a plugin, not a built-in.** If you are about to
add a vendor SDK to `go.mod` for a connector, that is the signal you are in the
wrong lane. See `docs/plans/connector-work-packages.md`.

## Boundaries

**Never set `port: 0`.** `lsof -ti :0` returns arbitrary system PIDs, read as a
false-positive "running" by the daemon monitor. Omit `port` for processes that
do not listen. Two guards hold this and neither should be removed:
`findPIDByPort` in `internal/service/service.go` refuses `port <= 0`, and
registry validation rejects it under
`TestValidateProjectConfigPortZeroIsError`.

**Changed source is not deployed source.** A `run_from: artifact` resource runs
an installed copy under `~/.cerberus/apps/<project>/<resource>/bin/`. `go
build`, `make build`, `go install`, `reload` and the console's Restart leave
that copy untouched; only `cerberus resource deploy <id>` rebuilds and
reinstalls it, and `cerberus resource status <id>` reports `artifact_stale`
with a next step. `resolveArtifactSource` in
`internal/connector/local/artifact.go` installs the build strategy's declared
`output`, falling back to `command[0]` without one — so a strategy missing an
`output` rule can install a binary the build never wrote.

**Never deploy the daemon through its own socket.** `deploy` or `ensure-fresh`
on the daemon resource restarts the daemon mid-operation: the call dies on EOF,
the artifact is left half-synced, and the launchd job can end up booted out
where `KeepAlive` will not bring it back. The serving runtime now refuses
resource mutations targeting itself. Build to a temp path, `mv` it over the
artifact, then `launchctl kickstart -k
gui/$(id -u)/com.fragments-engine.cerberus`.

**Do not reintroduce `selfexec.WatchAndExit` in `cerberus mcp`** (see the
comment in `cmd/cerberus/cmd_mcp.go`). Deploying the daemon replaces the binary
on disk, so every running `cerberus mcp` child would notice and exit — wiping
MCP access fleet-wide under hosts that do not respawn children. Each tool call
re-dials the socket, so the subprocess already survives daemon restarts.

**The daemon's environment is not your shell's.** launchd hands the daemon a
minimal `PATH` (`/usr/bin:/bin:/usr/sbin:/sbin`). Anything that shells out —
`go` for a build, `docker` for the Docker connector — must not assume a tool is
on `PATH` just because it resolves in your terminal. This has already produced
one silent outage: the Docker connector resolving `docker` once at boot,
failing, and caching that failure for the daemon's lifetime while
`cerberus connectors list` reported it healthy. Prefer explicit paths, fallback
search locations, and per-call resolution over boot-time resolution.

**A config that only exists on a branch is a config that disappears.**
Registered configs are referenced by absolute path, so checking out a branch
without them drops those projects from the runtime — resources vanish from
`resource list`, health and the console while the registry still points at the
path. Register a new config only once it is on `main`.

## Where the live config actually is

**The live registry on this machine is `~/.cerberus/config.yaml`**, and it
defines every running resource directly. Its header explains why: the
app-owned `<app>.cerberus.yaml` descriptors — including this repo's
`cerberus.cerberus.yaml` and `infrastructure.cerberus.yaml` — carry
`dir: /Users/chrispian/dev/hollis-labs/apps/<app>`, a different user and
directory layout. `dir:` is a literal path with no interpolation or override,
so **those descriptors are not registered and editing them changes nothing that
runs here.** Treat them as templates, not as live configuration.

The daemon itself is likewise not Cerberus-managed on this machine: it runs
from a hand-written `~/Library/LaunchAgents/com.fragments-engine.cerberus.plist`
with no `EnvironmentVariables`.

Verify before assuming — `cerberus resource list` and `cerberus project list`
report what is actually registered.

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
  a Cerberus DTO and never a vendor SDK type, and how to write the mapping.
  Short version: explicit mapping when the DTO exists to *exclude* something,
  codegen when it exists to *reshape* something. Read before writing one.
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

**A fresh checkout does not compile.** `internal/webui/dist` is gitignored and
`internal/webui/server.go` has `//go:embed all:dist`, which is a compile-time
error when the pattern matches nothing:

```
internal/webui/server.go:27:12: pattern all:dist: no matching files found
```

So `go build`, `make test` and therefore lefthook's pre-push hook all fail on a
clone until `make all` has produced the bundle once. Run `make all` first. After
that the milder rule applies: use `make all`, not `make build`, whenever `web/`
changes, or a plain build embeds whatever bundle is sitting there.

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

A resource that is not local/process is a **named handle for connector
operations**, not a broken workload — `muctlvaig` is server/ssh and has always
worked that way. Declaring `type: container` / `connector: docker` with a
`compose_file` is the supported pattern: `cerberus docker up <id>` resolves it
through the registry the way `cerberus ssh` does. Supervision-lane verbs report
such a resource as `unsupervised` and name the connector commands that do
operate it, rather than erroring or leaving a blank status.

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

A plugin declares its credentials in its manifest and the host resolves them
from the same provider the built-ins use, handing them over the `Init` config
channel keyed by secret name. A plugin never reaches the credential store and
never receives a secret it did not declare. Secrets deliberately do not travel
in the environment — `pluginLaunchEnv()` is an allow-list and adding a
credential to it would hand that value to every plugin, not the one that asked.

A missing credential is not fatal. The plugin loads, `plugin managed list`
reports it under `missing_secrets`, and an operation that actually needed it
fails as `credential_missing` with the recovery named. This matters concretely:
ContextForge's `get_health` is open and must keep working while `list_gateways`
401s, because that is how you tell a down tunnel from a down gateway.

**Built-in connector ids are reserved.** A plugin claiming `ssh`, `docker`,
`local` or `github` is refused at install — it would shadow the connector
Cerberus serves itself and, since the secret channel namespaces by connector id,
would be handed that connector's credentials.

## Work infrastructure is read-only

An infrastructure team administers the Adtran estate. Cerberus is a tool that
helps operate it, **not a control plane that owns it.** Connectors targeting
work resources implement read and probe operations; lifecycle and write
operations are documented as locked, with what would unlock them, rather than
built speculatively.

This is a scope decision, not a permissions workaround. Where a write operation
is genuinely wanted later, the ask goes to the team that owns the resource.

The exceptions already in place are deliberate and narrow: the local dev
services in `~/.cerberus/config.yaml`, and `muctlvaig` reached over SSH as the
operator's own account.

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

**Redaction runs over operator-facing error text, and it cannot read.**
`redact.Text` rewrites anything that parses as a credential on every error path.
It has eaten its own guidance six times: `Bearer JWT` became `Bearer
[REDACTED]`, `set CERBERUS_..._TOKEN` was swallowed as an assignment, and a
names-only `missing_secrets` field came back as `["[REDACTED]"]`. The redactor
has since learned to leave a non-token-shaped word after `Bearer` alone and to
skip fields that carry names by construction.

**Two live defects remain**, found by the capability audit and confirmed: the
`assignment` rule eats the word following the error code `credential_missing:`
— including the verb `reload` in a recovery instruction — and the `flag` rule
eats the word after `X-API-Key`, because that internal hyphen satisfies its
`--?` prefix. Neither is the `Bearer` case, which is genuinely fixed; these were
layered on top of it. Note also that redaction has no owning capability: it sits
on every surface's error path and therefore in no area's territory, which is why
each area saw only the damage visible from where it stood. The rule that remains: **do not
run redaction over a value that is a name by construction**, and if an error
message carries a recovery instruction, add a test that it survives `redact.Text`
intact. A safety net that eats the instruction is worse than no instruction.

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

The daemon itself is likewise not Cerberus-managed as a resource. It runs from
`~/Library/LaunchAgents/com.fragments-engine.cerberus.plist`, which is **not
hand-written** — it is byte-identical to what `cerberus install` emits from
`launchdPlistTemplate` in `cmd/cerberus/cmd_install.go`, and that template
declares no `EnvironmentVariables` at all.

That makes the minimal-PATH problem above a property of the shipped installer
rather than an artifact of this machine: **every `cerberus install` anywhere
produces a daemon that cannot find `go`.** The inconsistency is visible in our
own code — `internal/connector/local/launchd.go` emits `EnvironmentVariables`
for managed resources, so Cerberus knows how to give a launchd job an
environment and simply does not do it for its own daemon. The `DetectDocker`
fallback paths fixed the Docker symptom; the general case stands.

Verify before assuming — `cerberus resource list` and `cerberus project list`
report what is actually registered.

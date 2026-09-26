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
- `docs/plans/agent-authority-and-secrets.md` — what an agent is allowed to do
  with Cerberus's reach, what it may see, and what is recorded afterwards.
  Read before adding a credential backend, a redaction rule, or anything that
  gates an operation. It is also the honest inventory. The acknowledgment gate
  is an intent gate, not a human one. The audit log (`~/.cerberus/audit/`,
  append-only, hash-chained) covers the admin lane and plugin lifecycle.
  Resource mutators, pipelines and the audit CLI are still being added.
- `docs/plans/live-systems-security-target.md` — the end state that work
  converges on: effect classes, named targets, layered policy, human approval
  bound to a plan, egress labels, audit. Also lists gate defects to fix first
  and the decisions already taken. Read before adding a gate, a policy rule or
  an approval path.
- `~/Projects/tools` — ~70 shell scripts that already administer the work host,
  with their failure modes documented in-line. This is the capability spec for
  what connectors should grow; promote proven behaviour rather than redesigning.

`docs/handoffs/`, most of `docs/prompts/` (the Cerberus-specific ones —
`catalog-audit-generic.md` moved separately, it's reusable), and
`docs/validation/` were archived out of this repo to
`~/dev/agent-os/archive/cerberus/` in a docs cleanup pass: resolved
agent/operator handoffs, one-off task prompts, and a dated validation
snapshot. Three superseded `docs/plans/` drafts (`beta-release-plan.md`,
`cerberus-release-readiness-plan.md`, `gui-roadmap.md`) went the same way —
the beta they planned already shipped. `docs/adr/` and the active
`docs/plans/*` stay; they're still-read reference, not history.

## Commands

```bash
make test        # go test ./cmd/cerberus ./internal/... ./pkg/...
make build       # → bin/cerberus
make all         # web bundle into internal/webui/dist, then the binary
make lint        # go vet, golangci-lint, staticcheck, errcheck, govulncheck
make typecheck   # web/ TypeScript
```

A fresh checkout compiles. `internal/webui/dist` is gitignored and
`internal/webui/server.go` has `//go:embed all:dist`, which is a compile-time
error when the pattern matches nothing — so a clone used to fail `go build`,
`make test` and lefthook's pre-push hook before a contributor had any reason to
suspect the web build. A tracked `internal/webui/dist/.gitkeep` satisfies the
embed without committing the bundle. **Do not delete it**, and do not commit the
built bundle beside it. `emptyOutDir` wipes the directory on every build, so a
vite plugin in `web/vite.config.ts` writes the placeholder back after the bundle
is written; that is why the rule holds for a bare `npm run build` and not only
for `make`.

The milder rule still applies: use `make all`, not `make build`, whenever `web/`
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

**That is the whole core: `local`, `ssh`, `docker`, `github`.** Every provider
connector is a plugin. Ours live in `hollis-labs/cerberus-plugins`
(`cloudflare`, `digitalocean`, `namecheap`, `forge`, `contextforge`, `azure`,
`kubernetes`), and third-party plugins are standalone repos. The four providers
that used to be compiled in moved out in 2026-09
(`docs/plans/provider-plugin-extraction.md`), taking a stripped build from
62.8MB to 21.7MB with no provider SDK left in the host.

A plugin's operations reach every surface without host code. On the CLI,
`cerberus connectors exec <id> <op>` runs any operation, built-in or plugin,
with arguments typed from its schema. On MCP, a plugin operation is a generated
tool, `cerberus_<id>_<op>`, served only when the operator lists it under
`<id>: mcp: expose:` in `~/.cerberus/connector-config.yaml`; nothing is exposed
by default. On the API and in the console, it is the generic connector route.
Each call goes through `ExternalConnectorService.Execute`, the one path that
gates, refuses and audits.

**A new provider integration is a plugin, not a built-in.** If you are about to
add a vendor SDK to `go.mod` for a connector, that is the signal you are in the
wrong lane. See `docs/plans/connector-work-packages.md`.

A plugin declares its credentials in its manifest and the host resolves them
from the same provider the built-ins use, handing them over the `Init` config
channel keyed by secret name. A plugin never reaches the credential store and
never receives a secret it did not declare. Secrets deliberately do not travel
in the environment — `pluginLaunchEnv()` is an allow-list and adding a
credential to it would hand that value to every plugin, not the one that asked.

**The same rule covers a credential *handle*, not only a credential.**
`SSH_AUTH_SOCK` and the `DOCKER_*` variables used to sit in that allow-list, so
every loaded plugin could authenticate as the operator to any host trusting
their key and reach any configured Docker daemon, whether or not it had asked
for anything. They are now capabilities a plugin declares in its `plugin.yaml`
and the host grants — `ssh_agent` and `docker_socket`, defined in
`internal/pluginhost/capability.go`. A plugin that declares nothing receives
nothing, an unknown capability is refused at install, and `plugin managed list`
reports what each plugin declared and what it holds. Anything with that
character belongs in the capability vocabulary, not in the base allow-list.

A missing credential is not fatal. The plugin loads, `plugin managed list`
reports it under `missing_secrets`, and an operation that actually needed it
fails as `credential_missing` with the recovery named. This matters concretely:
ContextForge's `get_health` is open and must keep working while `list_gateways`
401s, because that is how you tell a down tunnel from a down gateway.

**Built-in connector ids are reserved.** A plugin claiming `ssh`, `docker`,
`local` or `github` is refused at install — it would shadow the connector
Cerberus serves itself and, since the secret channel namespaces by connector id,
would be handed that connector's credentials. The set comes from
`Registry.BuiltInIDs()`, plus `local`, which the supervision lane serves outside
the registry and so is reserved explicitly (`hostServedIDs`).

## Work infrastructure is not ours to change on our own say-so

An infrastructure team administers the Adtran estate. Cerberus is a tool that
helps operate it, **not a control plane that owns it.** Where a write against a
work resource is wanted, the ask goes to the team that owns that resource.

That rule governs what **we do** to work resources. It does not govern what a
connector **can do**. Cerberus is built in public, for operators whose estates
look nothing like ours, so a connector may implement write operations whether
or not any of our work targets will ever accept them. It implements them the
way every write here is built: `Destructive` and `SupportsDry`, behind `--ack`,
with a real preview. A connector that can write is not permission to write to a
work resource.

Today nothing stands between an acknowledged write and its target except
`--ack` and the target's own access control, and `--ack` is an intent gate, not
a human one. Per-target write policy, with a human approving where the target
calls for it, is the planned answer; see "Human-in-the-loop is a policy file
plus MCP elicitation" in `docs/plans/agent-authority-and-secrets.md`. Until then,
let the credential be the policy: point a connector at a work target with an
identity that cannot write — a read-only role on a work cluster — rather than
relying on nobody passing `--ack`.

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

**A request field that restricts or changes a mutation must not be ignorable
by an older daemon.** A daemon that does not know a JSON field ignores it and
runs the plain operation. So a field that asks for less than the operation
(plan only, dry-run-like) or binds it to something (a plan hash, a
confirmation) must travel on a route or an API version that an older daemon
refuses. It must never be an extra body field. In #103, `connectors plan` was
sent as `plan: true` on the operation's own route, so an older daemon would have
run the operation. It now uses `…/operations/{op}/plan`, which older daemons
answer with a 404. Test any such field against the old handler, as
`TestPlanRequestNeverRunsOnAnOlderDaemon` does. A field that licenses a call,
such as `approval_id` or `acknowledged`, is safe to add as a body field: an
older daemon that ignores it also predates the gate it satisfies.

**`DaemonUnreachableError` means the request was never delivered.** It is not
"the daemon looks down" — it is the token that licenses a caller to re-run an
operation in-process, so returning it for a post-delivery error silently
re-runs a mutation that the daemon may already have executed, with the serving
runtime's self-mutation guard bypassed because the retry never reaches the
serving runtime. Deploying the daemon produces exactly that error. Only a dial
failure qualifies (`requestNeverSent` in `internal/cerbapi/socket_client.go`);
EOF, connection reset and deadlines do not. The other half of the invariant is
in `cmd/cerberus/cmd_transport.go`: a mutation chooses its transport *before*
sending — `resourceMutationSocket` — so the fallback decision is made while
nothing is at stake. Reads may still attempt the daemon and fall back on
failure.

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

The sixth and seventh occurrences are also fixed: the `assignment` rule ate the
word following the error code `credential_missing:` — including the verb
`reload` in a recovery instruction — and the `flag` rule ate the word after
`X-API-Key`, because that internal hyphen satisfied its `--?` prefix. The
`assignment` rule now exempts Cerberus's own error codes (a name by
construction, like `missing_secrets`) while still redacting the text behind
them, and the `flag` rule requires a word boundary before the dash. Note that
redaction has no owning capability: it sits on every surface's error path and
therefore in no area's territory, which is why each area saw only the damage
visible from where it stood.

The eighth was a JSON key rather than prose: the Kubernetes plugin's
`credential_plugin` object came back wholly `[REDACTED]`, and was renamed
rather than patched. The ninth is fixed in the `assignment` rule. azidentity
prefixes an error with its credential's Go type name, `AzureCLICredential: `,
and a name ending in `Credential` or `Token` satisfied the rule, so the az CLI's
own `ERROR:` was eaten as if it were the credential's value. An UpperCamelCase
type name of two or more words, joined by `: ` to something that is not
token-shaped, is now read as prose. A token-shaped value after such a name is
still redacted. This one is in text Cerberus did not compose, a vendor SDK's
error, which is exactly where `Text` is the only net there is: value-boundary
redaction protects the values Cerberus resolved, not the grammar of someone
else's message.

The tenth was a placeholder Cerberus wrote itself. The Vercel deploy plan showed
its token as `--token [vercel token]`, and the `flag` rule read `[vercel` as the
token, so every plan the console showed came back as `--token [REDACTED]
token]`. It was fixed by changing the placeholder to `VERCEL_TOKEN=<vercel
token>`, not the rule, and a test holds the displayed command unchanged through
`Text`.

**Ten casualties, eight of them patched in the same regexes, was the finding**,
and WP-S2 is the structural fix it called for. `redact.Text` ran over rendered
prose and re-derived, from a regex, a key/value structure the caller had in its
hands and threw away. Credentials are now redacted at the value boundary, and
Cerberus's own messages are rendered once, from safe parts:

- **Values.** Every request carries a `redact.Scope`. `cerbapi.BeginRequest` and
  `BeginHTTPRequest` create it at each entry point. `app.ConnectorSecrets`
  registers each credential it resolves, and a plugin's load-time credentials
  merge in at `CallTool`. Every edge renders through the scope: the socket and
  console writers (through the response writer), the progress stream,
  `Execute`'s errors, MCP results, errors and notifications, the in-process CLI
  and the logs. The scope removes a resolved credential wherever it appears,
  with or without a label, whoever composed the message. This is the acceptance
  test: `TestResolvedCredentialNeverReachesAnySurface`, with a real vendor SDK
  echoing an unlabelled token.
- **Prose.** Cerberus's own refusals are `redact.Guidance`, or `redact.Prose`
  for text composed where redact cannot be imported. They are rendered once
  where they are made: the prose kept, a wrapped cause through the scope and the
  rules. A request remembers what it rendered, and an edge that meets exactly
  that text again only removes values. A client trusts daemon text as final only
  when the daemon marks the body `rendered: true`, so version skew falls back to
  the rules rather than skipping them. `redact.ErrorText` is how an edge with no
  scope, such as the CLI's `main`, shows an error.

**What `redact.Text` still covers** is text Cerberus did not resolve or compose:
vendor, remote and child output, the causes behind a Guidance, a credential a
tool holds itself (gh's login, docker's config, a `vercel login` session), a
plugin value under 8 bytes, and any path that has no scope. A secret with no
label in that text still gets through, which is why the value boundary exists.
A rule change can still eat vendor wording, but it can no longer eat an
instruction written as Guidance.

The rules that remain:

- **Do not run redaction over a value that is a name by construction.** Declare
  it: a secret that is a path or a name says `kind: path` or `kind: name`.
- **Write a refusal or recovery instruction as `redact.Guidance`, with names as
  its arguments**, never a provider's text. Add a test that it reaches the
  operator intact on every lane, as `TestConvertedRefusalsSurviveEveryLane`
  does. A safety net that eats the instruction is worse than no instruction.

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

That made the minimal-PATH problem a property of the shipped installer rather
than an artifact of this machine: every `cerberus install` produced a daemon
that could not find `go`. `launchdPlistTemplate` now emits an
`EnvironmentVariables` key carrying a `PATH` composed from the installing user's
environment — `daemonLaunchPath` in `cmd/cerberus/cmd_install.go` — rather than
hardcoding Homebrew paths that are wrong on Intel Macs and under MacPorts. Only
`PATH` is carried: secrets do not travel in the environment, and copying the
installing shell's whole environment into a persistent launchd job would do
exactly that. Entries are filtered to absolute paths, and launchd's own four
directories are kept as the tail.

**The plist on this machine predates that fix**, so the running daemon still has
the minimal `PATH` until it is reinstalled. Verify with
`ps eww -o command= -p $(pgrep -f 'cerberus daemon')` rather than assuming, and
keep writing connectors that resolve their tools explicitly and per call — the
installer fix raises the floor, it does not remove the rule above.

Verify before assuming — `cerberus resource list` and `cerberus project list`
report what is actually registered.

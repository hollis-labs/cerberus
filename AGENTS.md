# Cerberus

Cerberus is a single-binary Go control plane for the infrastructure its operator administers. It builds, deploys, supervises and inspects that infrastructure over one runtime service that the CLI, daemon socket, HTTP API, web console and MCP adapter all share. Every capability added to a connector becomes a CLI command, an API operation and an MCP tool at the same time, which is what makes it usable by agents as well as by people.

It owns execution and derived operational state — not the definitions a project writes about itself, the data it holds, or the credentials it needs. It is v2-only: `resources:` are the model, the legacy `services:` lane is frozen.

## This may manage real systems

**Work on this repo can change what happens to production systems.** An operator's Cerberus may reach real hosts, clusters and SaaS APIs, and a local test that calls a connector may write for real. Read this section before running anything.

- **Destructive connector operations require explicit acknowledgment (`--ack`)** and most support `--dry-run`. Use the preview first.
- **A test against a live integration can write for real.** Point tests at fakes, and treat a live target as production unless you know otherwise.
- **A Cerberus-started `ssh` has no TTY** and cannot answer a password or MFA prompt. An auto-restarting tunnel against an interactive auth endpoint is a good way to get an account locked out, so tunnel resources should be on/off with `auto_start` and `auto_restart` both false.
- **Your account may lack access a connector assumes**, for example not being in a remote host's `docker` group, in which case container introspection has to go over HTTP rather than `docker ps`.

Operator-specific notes (which hosts, which tenants) belong in the operator's own notes, never in this repo.

## Start Here

- `README.md` — install paths, CLI reference. `docs/install.md` has the install detail.
- `docs/plans/infra-admin-control-plane.md` — the current direction: Cerberus as the single place for ssh, file transfer, deploys and host administration. Read before adding connector capability.
- `docs/adr/0002-resource-only-local-workload-model.md` — why v2 is the only model, and what "frozen" means for `services:`.
- `docs/adr/0003-connector-response-dtos.md` — why a connector operation returns a Cerberus DTO and never a vendor SDK type, and how to write the mapping. Explicit mapping when the DTO exists to *exclude* something, codegen when it exists to *reshape* something.
- `internal/cerbapi/resource_runtime_service.go` — the shared runtime service for supervised local workloads. Behavior changes belong here, not in a caller.
- `internal/cerbapi/external_connector_service.go` — the imperative admin lane: per-call resolution, dry-run, acknowledgment, redaction, MCP tool generation. New administrative verbs belong here.
- `internal/domain/connector.go` — the interface every provider implements.
- `internal/connector/local/` — `dev_session.go` is one runtime mode; `os_service` is the other, with `launchd.go` (macOS) and `systemd.go` (Linux, a systemd user unit) as its supervisors. `artifact.go` owns the build→install join.
- `pkg/plugin/` — the public plugin authoring contract: `plugin.yaml` and MCP tool naming. A plugin outside this repo imports this, `pkg/connector` and `pkg/resource`; the host half stays in `internal/pluginhost`.
- `internal/registry/` — discovery and validation of per-repo `*.cerberus.yaml`.
- `docs/secrets.md` — how a resource names a credential without carrying one.
- `docs/plans/agent-authority-and-secrets.md` — what an agent may do with Cerberus's reach, what it may see, and what is recorded afterwards. Read before adding a credential backend, a redaction rule, or anything that gates an operation. The acknowledgment gate is an intent gate, not a human one. The audit log (`~/.cerberus/audit/`, append-only, hash-chained) covers the admin lane and plugin lifecycle.
- `docs/plans/live-systems-security-target.md` — the end state that security work converges on: effect classes, named targets, layered policy, human approval bound to a plan, egress labels, audit. Read before adding a gate, a policy rule or an approval path.

## Commands

```bash
make test        # go test ./cmd/cerberus ./internal/... ./pkg/...
make build       # → bin/cerberus
make all         # web bundle into internal/webui/dist, then the binary
make lint        # go vet, golangci-lint, staticcheck, errcheck, govulncheck
make typecheck   # web/ TypeScript
```

A fresh checkout compiles. `internal/webui/dist` is gitignored and `internal/webui/server.go` has `//go:embed all:dist`, which is a compile-time error when the pattern matches nothing. A tracked `internal/webui/dist/.gitkeep` satisfies the embed without committing the bundle. **Do not delete it**, and do not commit the built bundle beside it. `emptyOutDir` wipes the directory on every build, so a vite plugin in `web/vite.config.ts` writes the placeholder back after the bundle is written; that is why the rule holds for a bare `npm run build` and not only for `make`.

Use `make all`, not `make build`, whenever `web/` changes, or a plain build embeds whatever bundle is sitting there.

Lefthook runs the local gate; run `lefthook install` after cloning. Pre-commit runs gofmt/goimports, `golangci-lint --new` and `go vet` on staged Go; pre-push runs the full `go test`.

## Two lanes, and they are not interchangeable

**The supervision lane** (`resource_runtime_service.go`) is for long-lived local workloads: `auto_restart`, health probes, launchd, artifact staleness. It is hardcoded to local/process in roughly ten places, deliberately.

**The admin lane** (`external_connector_service.go`) is for imperative administration: run a verb against a remote system, get a result. Stateless, resolved per call.

Remote containers and remote hosts belong in the admin lane. Do not widen the supervision lane to reach them; see `docs/plans/infra-admin-control-plane.md`.

A resource that is not local/process is a **named handle for connector operations**, not a broken workload — a `server`/`ssh` resource has always worked that way. Declaring `type: container` / `connector: docker` with a `compose_file` is the supported pattern: `cerberus docker up <id>` resolves it through the registry the way `cerberus ssh` does. Supervision-lane verbs report such a resource as `unsupervised` and name the connector commands that do operate it, rather than erroring or leaving a blank status.

## Core or plugin

Cerberus ships as a single installed binary. Compiled in: `local`, `ssh`, `docker` — the primitives the control plane is built on, none of which carries a vendor SDK. Everything else that talks to a provider is a plugin: optional per user, its own release schedule, loaded at runtime without rebuilding the host.

**That is the whole core: `local`, `ssh`, `docker`.** Every provider connector is a plugin. First-party plugins live in `hollis-labs/cerberus-plugins`, and third-party plugins are standalone repos. No provider SDK is left in the host, and no provider-specific lane: the console's former Vercel deploy runner is the `vercel` plugin, and the console's credential editor lists what connectors declare. See `docs/plans/provider-plugin-extraction.md`.

A plugin's operations reach every surface without host code. On the CLI, `cerberus connectors exec <id> <op>` runs any operation, built-in or plugin, with arguments typed from its schema. On MCP, a plugin operation is a generated tool, `cerberus_<id>_<op>`, served only when the operator lists it under `<id>: mcp: expose:` in `~/.cerberus/connector-config.yaml`; nothing is exposed by default. On the API and in the console, it is the generic connector route. Each call goes through `ExternalConnectorService.Execute`, the one path that gates, refuses and audits.

**A new provider integration is a plugin, not a built-in.** If you are about to add a vendor SDK to `go.mod` for a connector, you are in the wrong lane. See `docs/plans/connector-work-packages.md`.

A plugin declares its credentials in its manifest and the host resolves them from the same provider the built-ins use, handing them over the `Init` config channel keyed by secret name. A plugin never reaches the credential store and never receives a secret it did not declare. Secrets deliberately do not travel in the environment: `pluginLaunchEnv()` is an allow-list, and adding a credential to it would hand that value to every plugin, not the one that asked.

**The same rule covers a credential *handle*.** `SSH_AUTH_SOCK` and the `DOCKER_*` variables are not in that allow-list. They are capabilities a plugin declares in its `plugin.yaml` and the host grants — `ssh_agent` and `docker_socket`, defined in `internal/pluginhost/capability.go`. A plugin that declares nothing receives nothing, an unknown capability is refused at install, and `plugin managed list` reports what each plugin declared and what it holds. Anything with that character belongs in the capability vocabulary, not in the base allow-list.

A missing credential is not fatal. The plugin loads, `plugin managed list` reports it under `missing_secrets`, and an operation that actually needed it fails as `credential_missing` with the recovery named. That matters: a health operation that needs no credential must keep working while an authenticated one fails, because that is how you tell a down tunnel from a down gateway.

**Built-in connector ids are reserved.** A plugin claiming `ssh`, `docker` or `local` is refused at install — it would shadow the connector Cerberus serves itself and, since the secret channel namespaces by connector id, would be handed that connector's credentials. The set comes from `Registry.BuiltInIDs()`, plus `local`, which the supervision lane serves outside the registry and so is reserved explicitly (`hostServedIDs`).

## Infrastructure you don't own is not yours to change on your own say-so

An operator's estate is often administered by other teams. Cerberus helps operate it; it is **not a control plane that owns it.** Where a write against someone else's resource is wanted, the ask goes to the team that owns that resource.

That rule governs what **an operator does** to a resource. It does not govern what a connector **can do**. Cerberus is built in public, for operators whose estates look nothing like each other, so a connector may implement write operations whether or not a given target will ever accept them. It implements them the way every write here is built: `Destructive` and `SupportsDry`, behind `--ack`, with a real preview. A connector that can write is not permission to write to a resource.

Let the credential be the policy as well: point a connector at a target you don't own with an identity that cannot write, such as a read-only role, rather than relying on policy or `--ack` alone.

## Boundaries

**Never set `port: 0`.** `lsof -ti :0` returns arbitrary system PIDs, read as a false-positive "running" by the daemon monitor. Omit `port` for processes that do not listen. Two guards hold this and neither should be removed: `findPIDByPort` in `internal/service/service.go` refuses `port <= 0`, and registry validation rejects it under `TestValidateProjectConfigPortZeroIsError`.

**Changed source is not deployed source.** A `run_from: artifact` resource runs an installed copy under `~/.cerberus/apps/<project>/<resource>/bin/`. `go build`, `make build`, `go install`, `reload` and the console's Restart leave that copy untouched; only `cerberus resource deploy <id>` rebuilds and reinstalls it, and `cerberus resource status <id>` reports `artifact_stale` with a next step. `resolveArtifactSource` in `internal/connector/local/artifact.go` installs the build strategy's declared `output`, falling back to `command[0]` without one — so a strategy missing an `output` rule can install a binary the build never wrote.

**Never deploy the daemon through its own socket.** `deploy` or `ensure-fresh` on the daemon resource restarts the daemon mid-operation: the call dies on EOF, the artifact is left half-synced, and the launchd job can end up booted out where `KeepAlive` will not bring it back. The serving runtime refuses resource mutations targeting itself. Build to a temp path, `mv` it over the artifact, then `launchctl kickstart -k gui/$(id -u)/com.hollis-labs.cerberus` (`com.fragments-engine.cerberus` on a daemon installed before the rename, until `cerberus install` migrates it). On Linux the last step is `systemctl --user restart com.hollis-labs.cerberus.service`.

**A request field that restricts or changes a mutation must not be ignorable by an older daemon.** A daemon that does not know a JSON field ignores it and runs the plain operation. So a field that asks for less than the operation (plan only, dry-run-like) or binds it to something (a plan hash, a confirmation) must travel on a route or an API version that an older daemon refuses, never as an extra body field. `connectors plan` therefore uses `…/operations/{op}/plan`, which older daemons answer with a 404. Test any such field against the old handler, as `TestPlanRequestNeverRunsOnAnOlderDaemon` does. A field that licenses a call, such as `approval_id` or `acknowledged`, is safe as a body field: an older daemon that ignores it also predates the gate it satisfies.

**`DaemonUnreachableError` means the request was never delivered.** It is the token that licenses a caller to re-run an operation in-process, so returning it for a post-delivery error silently re-runs a mutation the daemon may already have executed, with the self-mutation guard bypassed. Only a dial failure qualifies (`requestNeverSent` in `internal/cerbapi/socket_client.go`); EOF, connection reset and deadlines do not. The other half of the invariant is in `cmd/cerberus/cmd_transport.go`: a mutation chooses its transport *before* sending (`resourceMutationSocket`), so the fallback decision is made while nothing is at stake. Reads may still attempt the daemon and fall back on failure.

**Do not reintroduce `selfexec.WatchAndExit` in `cerberus mcp`** (see the comment in `cmd/cerberus/cmd_mcp.go`). Deploying the daemon replaces the binary on disk, so every running `cerberus mcp` child would notice and exit, wiping MCP access under hosts that do not respawn children. Each tool call re-dials the socket, so the subprocess already survives daemon restarts.

**The daemon's environment is not your shell's.** launchd hands the daemon a minimal `PATH` (`/usr/bin:/bin:/usr/sbin:/sbin`), and the systemd user manager its own compiled-in one; a daemon started outside a login session may also lack `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`, which `systemctl --user` and the keyring need (`launchenv.UserBusEnv` defaults them). Anything that shells out — `go` for a build, `docker` for the Docker connector — must not assume a tool is on `PATH` just because it resolves in your terminal. Resolving `docker` once at boot and caching the failure for the daemon's lifetime left `cerberus connectors list` reporting it healthy. Prefer explicit paths, fallback search locations, and per-call resolution over boot-time resolution.

**Anything an agent must act on goes in the MCP result's text, not its `_meta`.** Claude Code does not show a tool result's `_meta` to the model; only the content blocks reach it. The untrusted-text labels are therefore carried twice: as `_meta` for hosts that read it, and as a short second text block the model actually sees. The same applies to an approval's next step, a scope refusal, or any warning: if it exists only in `_meta` or a header, the agent never learns about it.

**A config that only exists on a branch is a config that disappears.** Registered configs are referenced by absolute path, so checking out a branch without them drops those projects from the runtime — resources vanish from `resource list`, health and the console while the registry still points at the path. Register a new config only once it is on `main`.

## Redaction

`redact.Text` runs over operator-facing error text, and it cannot read: it rewrites anything that parses as a credential, and it has repeatedly eaten Cerberus's own guidance (`Bearer JWT`, `set CERBERUS_..._TOKEN`, the word after `credential_missing:`, a names-only `missing_secrets` field, a credential-named JSON key in a WebAuthn challenge, a plugin id that contains `password`, and a declared secret's name under a `secrets` list when the secret had no description beside it). Patching the regexes each time was the wrong fix, so redaction is structural:

- **Values.** Every request carries a `redact.Scope`, created by `cerbapi.BeginRequest` / `BeginHTTPRequest` at each entry point. `app.ConnectorSecrets` registers each credential it resolves, and a plugin's load-time credentials merge in at `CallTool`. Every edge renders through the scope (socket and console writers, progress stream, `Execute` errors, MCP results and notifications, the in-process CLI, logs), which removes a resolved credential wherever it appears, labeled or not. Acceptance test: `TestResolvedCredentialNeverReachesAnySurface`, with a real vendor SDK echoing an unlabelled token.
- **Prose.** Cerberus's own refusals are `redact.Guidance`, or `redact.Prose` where redact cannot be imported. They are rendered once where they are made: the prose kept, a wrapped cause through the scope and the rules. A client trusts daemon text as final only when the daemon marks the body `rendered: true`, so version skew falls back to the rules. `redact.ErrorText` is how an edge with no scope, such as the CLI's `main`, shows an error.
- **What `redact.Text` still covers** is text Cerberus did not resolve or compose: vendor, remote and child output, a credential a tool holds itself, a plugin value under 8 bytes, and any path with no scope.

The rules:

- **Do not run redaction over a value that is a name by construction.** Declare it: a secret that is a path or a name says `kind: path` or `kind: name`, and an id Cerberus learns (connector, plugin) is registered with `redact.RegisterNames`.
- **A response field that is public by construction but credential-named is encoded or exempted by schema, never renamed to slip past.** Encode it as an opaque value, as `cerbapi.WebAuthnOptions` does, or exempt it where the schema proves it cannot hold a value (`namesOnlyKeys`; and, opt-in, a response a handler wraps in `redact.DeclaredSchema` because it serves Cerberus's own schema, connector definitions or the credential editor, whose declared secrets' `name`, `kind` and `env` are names while every sibling still gets the key check. The exemption is never inferred from a payload's shape, so operation data shaped like a definition gets the ordinary walk). If it is a real credential, it does not belong in the response: hand over a digest. Either way, test it through a real client (a browser, or the real socket client and response writer), because a lenient fake reads past the damage.
- **Write a refusal or recovery instruction as `redact.Guidance`, with names as its arguments**, never a provider's text, and add a test that it reaches the operator intact on every lane, as `TestConvertedRefusalsSurviveEveryLane` does. A safety net that eats the instruction is worse than no instruction.

## Where the live config is

**The live registry is `~/.cerberus/config.yaml`.** App-owned `<app>.cerberus.yaml` descriptors, this repo's `cerberus.cerberus.yaml` and `infrastructure.cerberus.yaml` included, only take effect once registered. A relative `dir:` (a local process resource's, or a pipeline action's) resolves against the directory the descriptor is in, so `dir: .` is the repo wherever it is checked out; `~/` is expanded in `dir`, `command`, `env`, `env_file` and the build strategy's paths. Write descriptors that way, with no `/Users/<name>` or `/home/<name>` in them, and they register unchanged on any host. An absolute `dir:` is taken as written.

A resource's plist or systemd unit gets the `PATH` the serving daemon runs with unless its `env:` sets one, so a descriptor needs neither `PATH` nor `HOME`.

The daemon itself is not Cerberus-managed as a resource by default. On macOS it runs from the launchd plist `cerberus install` writes from `launchdPlistTemplate` in `cmd/cerberus/cmd_install.go`; on Linux from the systemd user unit `com.hollis-labs.cerberus.service` that `cmd/cerberus/cmd_install_systemd.go` writes the same way, and `cerberus daemon status` reports `origin: systemd` for it. That template emits an `EnvironmentVariables` key carrying a `PATH` composed from the installing user's environment (`daemonLaunchPath`). Only `PATH` is carried: secrets do not travel in the environment, and copying the installing shell's whole environment into a persistent launchd job would do exactly that. Entries are filtered to absolute paths, and launchd's own four directories are kept as the tail.

A plist installed before that fix leaves the daemon with launchd's minimal `PATH` until it is reinstalled. Verify with `ps eww -o command= -p $(pgrep -f 'cerberus daemon')` rather than assuming, and keep writing connectors that resolve their tools explicitly and per call.

Verify before assuming — `cerberus resource list` and `cerberus project list` report what is actually registered.

## Contributing

Open a pull request against `main`; a maintainer will review it. `CONTRIBUTING.md` has the sequence.

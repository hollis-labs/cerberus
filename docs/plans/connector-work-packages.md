# Connector Work Packages

Self-contained briefs for execution agents extending the admin lane. Read
`docs/plans/infra-admin-control-plane.md` first for the direction and the
constraints; this file is the work breakdown.

Each package names its own files, its acceptance criteria, and what it must not
do. Take one package. Do not take two.

## Before you start

State as of 2026-09-17, so you do not have to rediscover it:

- `main` is at `41c76a2` and is green. `v0.4.0-beta.2` is the current tag and the
  **first** one carrying `pkg/plugin`. `v0.4.0-beta.1` is unusable from a plugin —
  it predates the module rename and declares `github.com/chrispian/cerberus`, so
  Go rejects it on a path mismatch. `go mod tidy` will try to resolve back to it;
  pin `v0.4.0-beta.2` explicitly.
- `hollis-labs/cerberus-plugins` exists, CI is green, and it fetches this private
  module with a scoped PAT in `HOLLIS_LABS_TOKEN`. No `replace` directives — do
  not add one.
- The ContextForge plugin is installed in the running daemon from
  `cerberus-plugins/dist/`, which is gitignored. `make clean` there is safe now
  (a missing plugin directory no longer kills the daemon) but the plugin will
  disappear from `connectors list` until `make dist` runs again.

### Working alongside other sessions

**Take a worktree.** `git worktree add ../cerberus-<wp> -b <branch>` gives you your
own index and checkout. Two sessions in one checkout share an index, and a bare
`git commit` or `git add -A` will sweep up whatever the other session has staged —
this happened twice, once producing a commit that did not compile.

Commit with explicit pathspecs (`git commit -- path/to/file`) and check
`git diff --cached --name-only` before committing if anything else might be live.

### Gates, and one that does not fire

- `make test` and `golangci-lint run --new-from-rev=main ./...` must both be clean.
- **`--new-from-rev` does not check formatting.** Run `gofmt -l .` separately. A
  module rename slipped three unformatted files past the lint gate precisely this
  way.
- Leave these alone, all pre-existing: `web/package-lock.json` (dirty in the
  working tree), and the gofmt drift in `internal/cerbapi/snapshot_recorder.go`
  and `internal/service/lockfile.go`. The repo has ~149 pre-existing lint issues;
  fixing them is not your package.

### Editing these docs

Use anchored replacements, not offset or index slicing. An index-based edit
silently deleted an entire work package from this file and it took a commit
audit to notice.

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
- **Return a Cerberus DTO, never a vendor SDK type** — see
  `docs/adr/0003-connector-response-dtos.md`. The DTO is an allow-list, so a
  vendor adding a credential field in a minor release cannot silently widen our
  output. This is not theoretical: `go-contextforge`'s `Gateway` carries
  `AuthToken`, `AuthPassword` and `OAuthConfig`, and the natural implementation
  of `list_gateways` would have emitted them into CLI output, MCP results and
  agent context.
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
WP-0 plugin gaps           DONE
WP-2 pkg/plugin promotion  DONE ──► WP-4 ContextForge  DONE ──► WP-7 DONE ──► WP-5 Azure DONE
WP-1 dry-run extraction    ──► WP-3 remote docker DONE ──► WP-6 docker resources DONE
                               (WP-1 was skipped, never blocking; still open)

WP-8 ssh elevation             still open ──┐
WP-9 recursive transfer    DONE           ──┴─► completes the tools/ port
WP-10 prove list_gateways      operator action; closes the ContextForge story

Deferred Azure — cost reporting, Key Vault, Resource Graph. All three probed
2026-09-17 and none is buildable yet: cost is RBACAccessDenied, no Key Vault
exists, and Resource Graph returns the same two resources as list_resources
because there is one visible subscription. Unlock conditions in WP-5.
```

WP-1 and WP-2 are independent of each other and both unblock work downstream.
WP-1 rewrites the 600-line `dryRunPreview` switch, so nothing else that touches
that file should run beside it. WP-2 is done, so WP-4 is unblocked.

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

*Superseded (P0-4):* the trust tiers and signing flags are gone. "Signed" was
self-asserted and treated the same as unsigned, so it implied vetting that
Cerberus does not do. A plugin now records an `origin` — `installed`, or `dev`
for a development install whose destructive operations are refused — and an
`entrypoint_sha256` fingerprint that is change detection, not trust.

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

## WP-2 — Promote the plugin authoring contract to `pkg/plugin` — DONE 2026-09-16

**Why this blocks everything plugin-shaped:** `internal/plugins/dockerplugin`
imports `github.com/hollis-labs/cerberus/internal/pluginhost`. An external module
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

**Module path — RESOLVED 2026-09-16.** The module was renamed from
`github.com/chrispian/cerberus` to `github.com/hollis-labs/cerberus` to match
the remote. `GOPRIVATE=github.com/hollis-labs/*` is already set, so a module
with repository access resolves it directly and **no `replace` directive is
needed**.

**Acceptance:** an operation runs against a non-default Docker host and the
result is demonstrably from that host. Permission failures name the host and
suggest the `docker` group.

### What shipped

`docker.Target` (`internal/connector/docker/target.go`) carries `Host` and
`Context`, is read out of each call's config by `TargetFromConfig`, and binds a
copy of the backend via `Backend.WithTarget`. Selection is per operation, and
`TestExternalConnectorServiceRoutesDockerOperationsToTheRequestedHost` asserts
the next call does not inherit the last one's host. Setting `DOCKER_HOST` uses
`append(os.Environ(), …)` as the brief warned; `--context` goes on as a global
flag before the subcommand. Surface: `--host`/`-H` and `--context` on all four
`cerberus docker` commands, `docker_host`/`docker_context` on the MCP tools, and
`host`/`context` in every operation's input schema.

Host and context are **refused together** rather than resolved. Verified
against docker 24.0.2: an explicit `--context` silently wins over `DOCKER_HOST`
and nothing says so, which for `destroy` is the worst possible place to be
wrong.

### The CLI will not tell you which host failed, or why

The brief said "connection refused with no host named is the failure mode to
avoid." It is worse than that. Every `ssh://` transport failure, whatever the
cause and whatever the host, is reported as one line:

```
Cannot connect to the Docker daemon at http://docker.example.com. Is the docker daemon running?
```

`docker.example.com` is a placeholder the CLI substitutes — the same string for
every host — and "is the docker daemon running?" is the wrong question when the
daemon is running and refusing. The real cause reaches stderr **only at
`--log-level debug`**, on a `commandconn (ssh):` line.

So the connector turns debug logging on for `ssh://` targets only, and
`failure.go` recovers the cause from it. Debug logging writes to stderr and
leaves stdout — the JSON every parser here reads — untouched, and stderr is
read only on a non-zero exit, so the success path pays nothing. `tcp://` and
`unix://` report usable errors on their own and stay on default logging.

Result against muctlvaig, where `cburks` is outside the `docker` group:

```
$ cerberus docker ps -H ssh://muctlvaig
Error: docker ps: ssh://muctlvaig: cannot connect to the Docker daemon: failed to
open the raw stream connection: dial unix /var/run/docker.sock: connect: permission
denied — the account can reach the host but not its Docker socket; add the account
to the docker group there (usermod -aG docker <user>, then reconnect) or run docker
under sudo (exit status 1)
```

The host, the cause and the recovery, none of which the CLI gives up on its own.
`TestSocketPermissionRecoverySurvivesRedaction` holds the AGENTS.md rule that
`redact.Text` must not eat that instruction, and checks target hosts survive too.

### Verified 2026-09-17

muctlvaig is the negative case by design — the brief already established the
account is not in its `docker` group, and the run above is that, end to end from
the connector.

For the positive case, `ssh://localhost` was **not** available (Remote Login is
off on this machine; `ssh localhost` is connection-refused), so a throwaway
Docker-in-Docker daemon on `tcp://127.0.0.1:12375` stood in as a genuinely
separate daemon with its own container. Two daemons, two answers, one Cerberus:

```
$ cerberus docker ps --context desktop-linux     # 3 containers, incl. the dind one
$ cerberus docker ps -H tcp://127.0.0.1:12375    # 1 container: wp3-proof
$ cerberus docker down wp3-proof -H tcp://127.0.0.1:12375
Container stopped: wp3-proof                     # default daemon unaffected
$ cerberus docker logs wp3-proof --context desktop-linux
Error: docker logs wp3-proof: docker context desktop-linux: Error response from
       daemon: No such container: wp3-proof (exit status 1)
```

Read and write paths, both selectable, each answer demonstrably from the daemon
that was asked. The same three cases went through `cerberus mcp` as
`cerberus_docker_ps` with `docker_host`, over a daemon socket, so the payload
and the error survive the transport. The dind container and image were removed
afterwards; the live daemon was never touched.

Two things worth knowing before repeating this:

- **The CLI proxies to the running daemon whenever its socket answers**, so a
  locally built binary tests nothing until you isolate it. `SocketPath()` derives
  from `$HOME`, so a sandbox `HOME` forces in-process execution — and a daemon
  needs a *short* one, because a unix socket path caps near 104 bytes.
- **A sandbox `HOME` also breaks docker's own context lookup**, which is where
  `desktop-linux` lives. `DOCKER_CONFIG=~/.docker` restores it — and that it
  works at all is the `append(os.Environ(), …)` rule paying off in the open.

**Not done, deliberately:** `cburks` is still outside the `docker` group on
muctlvaig, and that is not ours to grant (see Open Questions in the control
plane plan). Introspect ContextForge over HTTP there, as `tools/` does. The
Azure box should add the user to the `docker` group at provisioning time.

---

## WP-3 — Remote Docker over SSH *(core connector)* — DONE 2026-09-17

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

### Ground already covered, verified 2026-09-17

**`CLIBackend.run` sets no `cmd.Env`**, so every `docker` invocation inherits the
daemon's environment:

```go
cmd := exec.CommandContext(ctx, c.dockerPath, args...)
```

Setting a per-operation `DOCKER_HOST` means setting `cmd.Env` — and **`cmd.Env`
replaces rather than extends**. `cmd.Env = []string{"DOCKER_HOST=…"}` would strip
`HOME` and break docker's own config and credential lookup. Use
`append(os.Environ(), "DOCKER_HOST="+host)`.

**The daemon's environment has what `ssh://` needs**, which was not obvious:

```
HOME=~   USER=cburks   SSH_AUTH_SOCK=/private/tmp/com.apple.launchd…
PATH=/usr/bin:/bin:/usr/sbin:/sbin
```

So `~/.ssh/config` is readable, the agent socket is present, and docker's ssh
transport shells out to `/usr/bin/ssh`, which is on that minimal PATH. The
daemon already reaches muctlvaig — `cerberus ssh status muctlvaig` returns
`reachable: true` from the daemon, not just from a shell.

Binary discovery is solved; `DetectDocker` and `CERBERUS_DOCKER_PATH` already
landed. Do not re-litigate it.

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

### What shipped

`docker.Target` (`internal/connector/docker/target.go`) carries `Host` and
`Context`, is read out of each call's config by `TargetFromConfig`, and binds a
copy of the backend via `Backend.WithTarget`. Selection is per operation, and
`TestExternalConnectorServiceRoutesDockerOperationsToTheRequestedHost` asserts
the next call does not inherit the last one's host. Setting `DOCKER_HOST` uses
`append(os.Environ(), …)` as the brief warned; `--context` goes on as a global
flag before the subcommand. Surface: `--host`/`-H` and `--context` on all four
`cerberus docker` commands, `docker_host`/`docker_context` on the MCP tools, and
`host`/`context` in every operation's input schema.

Host and context are **refused together** rather than resolved. Verified
against docker 24.0.2: an explicit `--context` silently wins over `DOCKER_HOST`
and nothing says so, which for `destroy` is the worst possible place to be
wrong.

### The CLI will not tell you which host failed, or why

The brief said "connection refused with no host named is the failure mode to
avoid." It is worse than that. Every `ssh://` transport failure, whatever the
cause and whatever the host, is reported as one line:

```
Cannot connect to the Docker daemon at http://docker.example.com. Is the docker daemon running?
```

`docker.example.com` is a placeholder the CLI substitutes — the same string for
every host — and "is the docker daemon running?" is the wrong question when the
daemon is running and refusing. The real cause reaches stderr **only at
`--log-level debug`**, on a `commandconn (ssh):` line.

So the connector turns debug logging on for `ssh://` targets only, and
`failure.go` recovers the cause from it. Debug logging writes to stderr and
leaves stdout — the JSON every parser here reads — untouched, and stderr is
read only on a non-zero exit, so the success path pays nothing. `tcp://` and
`unix://` report usable errors on their own and stay on default logging.

Result against muctlvaig, where `cburks` is outside the `docker` group:

```
$ cerberus docker ps -H ssh://muctlvaig
Error: docker ps: ssh://muctlvaig: cannot connect to the Docker daemon: failed to
open the raw stream connection: dial unix /var/run/docker.sock: connect: permission
denied — the account can reach the host but not its Docker socket; add the account
to the docker group there (usermod -aG docker <user>, then reconnect) or run docker
under sudo (exit status 1)
```

The host, the cause and the recovery, none of which the CLI gives up on its own.
`TestSocketPermissionRecoverySurvivesRedaction` holds the AGENTS.md rule that
`redact.Text` must not eat that instruction, and checks target hosts survive too.

### Verified 2026-09-17

muctlvaig is the negative case by design — the brief already established the
account is not in its `docker` group, and the run above is that, end to end from
the connector.

For the positive case, `ssh://localhost` was **not** available (Remote Login is
off on this machine; `ssh localhost` is connection-refused), so a throwaway
Docker-in-Docker daemon on `tcp://127.0.0.1:12375` stood in as a genuinely
separate daemon with its own container. Two daemons, two answers, one Cerberus:

```
$ cerberus docker ps --context desktop-linux     # 3 containers, incl. the dind one
$ cerberus docker ps -H tcp://127.0.0.1:12375    # 1 container: wp3-proof
$ cerberus docker down wp3-proof -H tcp://127.0.0.1:12375
Container stopped: wp3-proof                     # default daemon unaffected
$ cerberus docker logs wp3-proof --context desktop-linux
Error: docker logs wp3-proof: docker context desktop-linux: Error response from
       daemon: No such container: wp3-proof (exit status 1)
```

Read and write paths, both selectable, each answer demonstrably from the daemon
that was asked. The same three cases went through `cerberus mcp` as
`cerberus_docker_ps` with `docker_host`, over a daemon socket, so the payload
and the error survive the transport. The dind container and image were removed
afterwards; the live daemon was never touched.

Two things worth knowing before repeating this:

- **The CLI proxies to the running daemon whenever its socket answers**, so a
  locally built binary tests nothing until you isolate it. `SocketPath()` derives
  from `$HOME`, so a sandbox `HOME` forces in-process execution — and a daemon
  needs a *short* one, because a unix socket path caps near 104 bytes.
- **A sandbox `HOME` also breaks docker's own context lookup**, which is where
  `desktop-linux` lives. `DOCKER_CONFIG=~/.docker` restores it — and that it
  works at all is the `append(os.Environ(), …)` rule paying off in the open.

**Not done, deliberately:** `cburks` is still outside the `docker` group on
muctlvaig, and that is not ours to grant (see Open Questions in the control
plane plan). Introspect ContextForge over HTTP there, as `tools/` does. The
Azure box should add the user to the `docker` group at provisioning time.

---

---

## WP-4 — ContextForge plugin *(first real plugin)* — DONE 2026-09-16

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

### SDK surface, verified 2026-09-16

`NewClient(httpClient *http.Client, address string, bearerToken string)` — base
URL is a plain constructor argument, which is what the tunnel requirement needs,
and auth is bearer JWT, matching what our probes found.

Eight services hang off the client: `Tools`, `Resources`, `Gateways`, `Servers`,
`Prompts`, `Agents`, `Teams`, `Cancel`. Each of the first six follows the same
shape — `List`, `Get`, `Create`, `Update`, `Delete`, `Toggle`/`SetState` — plus
extras: `Gateways.RefreshTools`, `Servers.ListTools`/`ListResources`/
`ListPrompts`, `Resources.ListTemplates`, `Agents.Invoke`.

**Route compatibility is confirmed against the live gateway.** Every path the
SDK targets answers `401` rather than `404`, so the routes exist and only auth
is missing — the SDK and the deployed CF agree on the API shape:

```
/health 200   /version 401  /gateways 401  /servers 401  /tools 401
/prompts 401  /resources 401  /a2a 401  /teams 307  /openapi.json 401
/resources/templates/list 401   /cancellation/status/x 401
```

Note the SDK is tested against **ContextForge v1.0.0-BETA-2**
(`mcpgateway==1.0.0b2`); confirm the deployed version once a token is in hand.
`/teams` answers 307, so that service redirects — the SDK uses trailing slashes
on team sub-paths and none on the collection.

### ⚠ `Gateway` carries live credentials — redact before returning

The `Gateway` type includes `AuthToken`, `AuthPassword`, `AuthHeaderValue`,
`AuthValue`, `AuthUsername`, `AuthHeaders` and `OAuthConfig`. `types.go` has 32
secret-bearing field references in total.

A `list_gateways` that marshals the SDK struct straight out **will leak upstream
auth tokens** into CLI output, MCP tool results and agent context. This is the
single most likely way this package causes real harm.

This is the case that produced `docs/adr/0003-connector-response-dtos.md`; read
it before writing the first operation.

So: **define our own response DTOs; never return the SDK type directly.** For a
gateway, return id, name, URL, transport, enabled, reachable, and `auth_type`
only — the *kind* of auth configured, never the value. That matches the
`probe-*` convention already in use: names, never values. Add a test that
asserts a gateway response containing a populated `AuthToken` does not serialize
it.

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

**Install:** `cerberus connectors plugin managed install <dir>`, which records
`origin: installed`. (Before P0-4 this recorded `trust_tier: unsigned`.)

### The `go.mod` an external plugin needs

```
module github.com/hollis-labs/cerberus-plugins/<name>

go 1.26.3

require (
	github.com/hollis-labs/cerberus v0.0.0-...
	github.com/hollis-labs/plugin-sdk v0.4.0
)
```

No `replace` directive: the module path now matches the remote and
`GOPRIVATE=github.com/hollis-labs/*` covers it, so `go get` works for anyone
with repository access. A plugin developed beside a local checkout can still add
a `replace` for convenience; CI does not need one.

A plugin imports `pkg/connector`, `pkg/resource`, `pkg/plugin` and
`plugin-sdk/subprocess` — and nothing else from Cerberus. If a plugin needs
something from `internal/`, that is a signal the authoring contract is missing a
piece, not that the plugin should reach in.

**Outcome:** shipped in `hollis-labs/cerberus-plugins` (`contextforge/`) with
four read-only operations. `get_health` verified live through the tunnel;
`list_gateways` is unproven because no ContextForge JWT exists on this machine —
it returns an actionable 401 naming the keychain path. The `Backend` interface
returns DTOs rather than vendor types, so `cf.*` is confined to two files and a
connector *structurally cannot* return a credential-bearing struct — stronger
than ADR 0003 required, and worth copying in WP-5.

Two host gaps it exposed, both now handled: `redact.Text` ate the auth guidance
in its own error message (fixed), and plugins have no host secret channel at all
(WP-7).

**Acceptance:** the plugin installs unsigned, loads, and
`cerberus connectors plugin managed exec contextforge list_gateways` returns the
real upstream registrations through the tunnel. Writes verified by `--dry-run`
only unless explicitly cleared to write.

---

## WP-5 — Azure plugin *(read and probe only)* — DONE 2026-09-17

**Scope decided 2026-09-17, narrowed from the original lifecycle plan.**

Cerberus is not taking over management of work infrastructure. An infrastructure
team administers the Azure estate; the goal here is a tool that helps, not a
control plane that owns. So this package implements **read and probe
operations only**. Write operations are documented below and deliberately not
built — see "Locked paths".

**Where:** `hollis-labs/cerberus-plugins`, directory `azure/`. Follow the
ContextForge plugin as the reference: `Backend` interface, SDK behind it,
secrets through the host channel, DTOs per ADR 0003.

**Library:** `github.com/Azure/azure-sdk-for-go`. `azidentity`'s
`AzureCLICredential` already works with no configuration — verified against the
live subscription 2026-09-17.

### What the environment actually is — probed, not assumed

The reachable subscription is `MCA-subscription-qualitymgmt`
(`1010d0a6-b5f9-4da3-99a2-4dd5cd3ea136`), and it is **not a compute
subscription**. Its entire resource inventory is:

```
1  Microsoft.CognitiveServices/accounts            PCB-Drawings-Extraction (AIServices, S0)
1  Microsoft.CognitiveServices/accounts/projects
```

with one model deployment:

```
Name             Model            Version   Sku
claude-sonnet-5  claude-sonnet-5  2         GlobalStandard
```

Registered providers are Storage, KeyVault, CognitiveServices, DocumentDB,
Search, Web, CostManagement, insights. **`Microsoft.Compute` and
`Microsoft.Network` are both NotRegistered.**

So a VM-shaped connector would have nothing to talk to. Build for what is there.

### Operations

All read-only, none destructive, none needing `--ack`:

1. `list_subscriptions` / `get_subscription` — id, name, state, tenant
2. `list_resource_groups` — name, location
3. `list_resources` — type, name, resource group; the inventory above
4. `list_ai_accounts` — Cognitive Services / AI Services accounts: name, kind,
   sku, endpoint
5. `list_model_deployments` — deployment name, model, version, sku. This is the
   operationally interesting one: it answers "what models can we actually call,
   at what version" without opening the portal, and it is adjacent to the agent
   work the rest of the portfolio is about.

**Never return a key.** Cognitive Services accounts have listable keys; the
account DTO exposes the endpoint and whether keys exist, never a value. ADR 0003
applies — and here the vendor type will hand you keys if you ask, so do not ask.

### Locked paths — document, do not implement

These are the operations the original brief called for. They are blocked, and
the block is not a code problem:

| Operation | Blocked by |
|---|---|
| `list_vms`, `get_vm` | `Microsoft.Compute` NotRegistered — no VMs can exist |
| `start_vm`, `deallocate_vm` | same, plus needs Virtual Machine Contributor |
| `create_vm`, `delete_vm` | same, plus `Microsoft.Network`, plus Contributor on a resource group |

**What would unlock them**, for whoever picks this up later:

- An admin runs `az provider register -n Microsoft.Compute` and
  `-n Microsoft.Network` at subscription scope. Requires subscription
  Contributor or Owner; there is no narrower built-in role that grants only
  registration. Verified failing 2026-09-17:
  `AuthorizationFailed … does not have authorization to perform action
  'Microsoft.Compute/register/action'`.
- **Contributor scoped to one resource group** for VM management.
  `Virtual Machine Contributor` alone is *not* sufficient to create a VM — it
  does not cover the VNet, NIC and public IP a new VM attaches to. Scoping to a
  single resource group is what makes it least-privilege, not picking narrower
  role names.
- Note the governance question rather than assuming it away: this is a shared
  quality-management subscription. Putting a persistent billable VM in it is a
  decision for whoever owns it, and a sandbox subscription would be the better
  home.

Write these in the plugin's README as "not implemented, here is why", so the
next person finds the reason instead of the gap.

### Acceptance

`cerberus connectors plugin managed exec azure list_model_deployments` returns
the `claude-sonnet-5` deployment above, live. Account DTOs carry no key
material, with a test asserting it the way `contextforge/dto_test.go` does.
Unit tests run against a fake `Backend` with no network.

### What shipped

`hollis-labs/cerberus-plugins`, directory `azure/` — the second plugin in that
repo, so it also proves the layout generalises. Registered in the root
`Makefile`, the CI matrix and the repo README.

Six read-only operations: the five above, with `get_subscription` split out from
`list_subscriptions` because they answer different questions. `get_subscription`
with no argument resolves the subscription every other operation will act on,
which is the cheapest way to confirm *which estate* Cerberus is reading before
wondering why a resource is missing. `subscription_id` is accepted by every
operation, so reading a second subscription needs no config change.

Nothing is `Destructive` and nothing `SupportsDry`, and a test asserts that
rather than leaving it to review — this connector's read-only scope is the whole
point of the package, so it should fail a build, not a code review.

The `Backend` interface returns Cerberus DTOs rather than vendor types, copying
what WP-4 concluded: `arm*` appears in exactly two files and a connector
operation *structurally cannot* return a credential-bearing struct.

Authentication is the signed-in Azure CLI user by default — no stored credential
at all. A service principal is supported through `tenant_id`/`client_id` config
and a `client_secret` declared in the manifest and resolved by the host secret
channel WP-7 built. A **half**-configured service principal is refused at load
rather than falling back silently: reading the estate as an unexpected identity
is worse than not reading it.

### `az` is not on the daemon's PATH, and azidentity has no lever but PATH

The one real trap in this package, and it is the `AGENTS.md` Docker-connector
rule almost exactly.

`AzureCLICredential` authenticates by shelling out to `az`. Verified on this
machine: the daemon's environment is `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, a
plugin subprocess inherits precisely that through `pluginLaunchEnv()`, and
Homebrew's `az` is at `/opt/homebrew/bin/az`. So the credential that "works with
no configuration" in a terminal fails under the daemon — which is where every
real call happens.

`AzureCLICredentialOptions` has no field for the binary's location. The only
lever is `PATH`. The plugin therefore searches a fallback list and prepends the
directory it finds to its own `PATH`, **on every credential build rather than
once at boot**, and returns the failure rather than remembering it. Caching that
one failure is what let the Docker connector report itself healthy for a
daemon's lifetime.

Anything else that grows an Azure operation inherits this: assume no developer
tool is on `PATH`.

### `managed load` does not restart a running plugin subprocess

Rebuilding `dist/` and running `cerberus connectors plugin managed load azure`
leaves the *already running* subprocess in place, so the next `exec` is served by
the old binary. The symptom is an error message you just fixed coming back
verbatim. `unload` then `load`.

### Verified 2026-09-17

Installed unsigned from `dist/azure`, loaded into the running daemon, and every
operation executed live against `MCA-subscription-qualitymgmt`:

- `list_model_deployments` returns the `claude-sonnet-5` GlobalStandard
  deployment, version 2, capacity 5000 — the acceptance criterion, through the
  daemon socket, which means the PATH repair above is exercised for real.
- `list_subscriptions`, `get_subscription`, `list_resource_groups` (5 groups),
  `list_resources` (the 2 resources above) and `list_ai_accounts` all return.
- `managed health azure` → `azure cli credential reads subscription
  MCA-subscription-qualitymgmt (1010d0a6…), state Enabled`.
- `cerberus connectors list` shows `azure  server  yes  6`.
- Error paths: an unknown account answers `no AI account named "nope" …
  Accounts present: PCB-Drawings-Extraction`; a bad subscription answers
  `not found (404, SubscriptionNotFound)` and points at `list_subscriptions`
  rather than at a listing that needs a good subscription to run.
- `Microsoft.Compute` and `Microsoft.Network` re-confirmed `NotRegistered`, so
  the locked paths above are still locked.

41 unit tests, all against a fake `Backend`; no test touches Azure. Among them:
an account populated with every credential the vendor type can hold serializes
none of them, and the recovery instructions in the error paths survive the two
`redact.Text` patterns that have eaten guidance before.

**Not verified:** the service principal path. No service principal exists to test
against, and creating one is a write against a subscription we do not own. The
code path is unit-tested for selection and refusal; its first live use will be
its first live use.

## WP-6 — Make docker resources real, and stop misreporting unsupervised kinds — DONE 2026-09-17

**Decided 2026-09-16, replacing an earlier "reject `type: container`" plan.**

The original framing was wrong and the live config proves it. `muctlvaig` is
`type: server` / `connector: ssh` — registered, listed, and deliberately not
supervised:

```
$ cerberus resource status muctlvaig
Error: resource "muctlvaig" is server/ssh; status currently supports local
       process resources only

$ cerberus ssh exec muctlvaig --ack -- 'id'      # works
```

So "validates clean, then fails every runtime operation" is equally true of a
resource that works as intended. `cmd_ssh.go` calls `findResource` to pull
`host`, `user` and `key_file` from config, which is exactly why
`cerberus ssh exec muctlvaig` needs no connection details. A `type: container`
resource is the same shape: a named handle for connector operations, not a
supervised workload. Rejecting it would be inconsistent and would foreclose a
useful pattern.

The real defect is the opposite one.

### 1. Docker's CLI ignores the registry

Every other connector CLI resolves the resource; docker synthesizes one:

```go
// cmd_docker.go
cfg := dockerResourceConfig(resourceID, composeFile)   // {id, name, container}
```

So `cerberus docker up web` cannot find a `compose_file` from config and `-f`
must be passed every time. Make `docker up`/`down`/`logs` resolve through
`findResource` the way ssh does, falling back to treating the argument as a
literal container name when no resource matches — that keeps
`cerberus docker logs <container>` working for containers that were never
declared.

This is what makes `type: container` resources worth declaring:

```yaml
- id: mtbf-monitor
  type: container
  connector: docker
  config:
    compose_file: ~/Projects/mtbf-monitor/docker-compose.yml
```

→ `cerberus docker up mtbf-monitor`.

### 2. The supervision lane misreports unsupervised kinds

`resource status` on a server or container resource reads like a defect. It
should say what is true: this kind is administered through its connector, not
supervised. Name the command — `cerberus ssh …`, `cerberus docker …`. Same for
the blank `STATUS` column in `resource list`; "unsupervised" beats an empty cell
that looks like a probe failure.

Optionally add a validation **warning** (never an error) noting that a
container resource is admin-lane, not supervised.

**Acceptance:** a `type: container` resource declared in config can be brought
up and down by id with no `-f`; `resource status` on it explains rather than
errors; `cerberus docker logs <name>` still works for an undeclared container.

**Do not:** reject container resources, or widen
`resource_runtime_service.go` to supervise them.

### What shipped

**Docker resolves the registry.** `dockerOperationConfig` in `cmd_docker.go`
looks the argument up through `registry.ResolveConfig` — the same registry
`cerberus ssh` reads, reached via the existing `loadResource` path rather than
by building an `app.App` — and falls back to a literal container name on a miss.
An explicit `-f` or `--host` beats whatever the resource declared.

Three cases, deliberately different:

| Argument | Behaviour |
|---|---|
| a declared `connector: docker` resource | its config drives the operation |
| no resource of that id | treated as a literal container name |
| a resource on another connector | refused, naming that connector's commands |

The third is the one worth arguing for. `cerberus docker up muctlvaig` on a
server/ssh resource would otherwise fall through to the literal path, look for a
container called `muctlvaig`, and fail with something unrelated to the mistake.

A compose-only resource has no single log stream, so `docker logs <id>` on one
says that and points at `docker ps`, rather than failing on a missing
`container` field.

**Unsupervised kinds report as `unsupervised`.** `internal/cerbapi/unsupervised.go`
holds the vocabulary — `SupervisedLocally`, the status word, the reason, and the
per-connector next step — so the CLI, the DTO and the console agree:

- `resource status` on a server or container resource now **answers**, with the
  kind, the connector and the commands that operate it.
- The nine other supervision-lane verbs still refuse, but through
  `UnsupervisedOperationError`, which names what to run instead. Nine messages
  saying "supports local process resources only" next to a `status` that
  explains would have been incoherent.
- `resource list` prints `unsupervised` where the STATUS cell was blank. A blank
  cell is indistinguishable from a probe that failed, which is exactly how a
  working resource came to read as broken.
- The console's runtime tally **skips** them. Its switch defaults to "stopped",
  so leaving them in would have moved the misreport rather than fixed it.

```
$ cerberus resource status muctlvaig
Status:      unsupervised
Context:     server/ssh resources are administered through the ssh connector,
             not supervised by the local runtime lane
Next Step:   cerberus ssh status muctlvaig | cerberus ssh exec muctlvaig -- <command>

$ cerberus resource deploy wp6-stack
Error: deploy does not apply to "wp6-stack": container/docker resources are
       administered through the docker connector, not supervised by the local
       runtime lane; try: cerberus docker up wp6-stack | ...
```

### The optional validation warning, deliberately skipped

The brief offered a validation **warning** on container resources. It is not
worth having, and adding it would contradict this package's own thesis.

WP-6 exists because a container resource is a *legitimate declaration*, the same
shape as the server/ssh resource that has worked all along. A warning on every
correct declaration is noise, and `cerberus validate` would start complaining
about `muctlvaig` too or be inconsistent about which unsupervised kinds it
minds. The information an operator actually needed — this is administered, not
supervised, and here is the command — now lives where they look for it, in
`status` and `list`.

### Verified 2026-09-17

Against a throwaway compose stack in a scratch config, so nothing touched the
running `mtbf-monitor` stack:

```
$ cerberus docker up wp6-stack            # no -f
Compose stack started: …/wp6/docker-compose.yml
$ docker ps --filter name=wp6
wp6-probe-1   alpine:3   Up Less than a second

$ cerberus docker logs wp6-probe-1 --lines 3   # undeclared container, still works
$ cerberus docker logs wp6-stack
Error: resource "wp6-stack" declares a compose stack and no single container;
       run `cerberus docker ps` and pass a container name from the stack

$ cerberus docker up muctlvaig
Error: resource "muctlvaig" is server/ssh, not a docker resource; try:
       cerberus ssh status muctlvaig | cerberus ssh exec muctlvaig -- <command>

$ cerberus resource list
ID         TYPE       CONNECTOR  STATUS
muctlvaig  server     ssh        unsupervised
wp6-stack  container  docker     unsupervised

$ cerberus docker down wp6-stack          # no -f; stack gone
```

The fixture was removed afterwards. `AGENTS.md` lost its "validates clean, then
fails on every runtime operation" trap note, which this package makes false.

---

## WP-7 — A host secret channel for plugins — DONE 2026-09-16

**Found by WP-4. Done before WP-5, which now inherits a working channel.**

**Why:** `docs/secrets.md` is the documented Cerberus secret story — a resource
names a credential, `keychain://` or `helper://`, and the host resolves it. That
story is not true for plugins. The host hands a plugin nothing:

```go
// internal/pluginhost/manager.go
Config:    map[string]string{},          // unconditionally empty
```

and `pluginLaunchEnv()` is a fixed allow-list with no credential entries. So the
ContextForge plugin resolves its own JWT from the keychain. That works and keeps
the value out of config, but it means every plugin reimplements secret
resolution and `connector-secrets.yaml` never reaches a plugin at all.

This is a contract gap, not a convenience gap: what we tell users about secrets
is false for plugins.

**Do:** resolve a plugin's declared secrets host-side and pass them over the
existing `Init` config channel. The manifest already declares secrets —
`contract.ConfigSchema.Secrets` carries name, description and env — so the host
knows what to resolve without the plugin asking.

**Design constraints, all of which matter:**

- **Resolve from the same provider built-in connectors use**, so
  `connector-secrets.yaml` and `keychain://` work identically either side of the
  plugin boundary.
- **Pass values through `Init`, not the environment.** The env allow-list exists
  because a subprocess inherits ambient env; adding credentials to it would leak
  them to every plugin rather than the one that declared the secret.
- **A plugin receives only the secrets its own manifest declares.** Never the
  whole store.
- **Never log a resolved value**, including in the init payload on a debug path.
  See `docs/adr/0003-connector-response-dtos.md` for the reasoning.
- **A missing secret must not fail the load.** The plugin should start and its
  operations should fail with the actionable `credential_missing` error the
  built-in connectors already produce. This mirrors the lesson from the restore
  bug: optional components must not be able to take the host down.

**Acceptance:** the ContextForge plugin drops its own keychain lookup, declares
`token` in its manifest, receives it from the host, and `list_gateways` runs
live. A plugin whose secret is absent loads and reports `credential_missing`.

**Do not:** widen `pluginLaunchEnv()` to carry credentials.

### What shipped

`Manager.Load` resolves the secrets a plugin's manifest declares, looking each
one up as `<plugin id>/<secret name>` through `internal/app.ConnectorSecrets` —
the same provider the built-in connectors resolve through — and hands the values
to `plugin/init` keyed by the manifest secret name. `internal/pluginhost/
secrets.go` is the whole channel. `pluginLaunchEnv()` is unchanged.

Two decisions worth knowing before building on this:

- **A missing secret does not pre-empt the operation, it explains the failure.**
  The manifest maps secrets to the connector, not to an operation, so the host
  cannot tell which operation needs which credential. Gating every operation
  would have broken ContextForge's `get_health`, which is open and is the
  fastest way to tell a down tunnel from a down gateway. Instead the host
  records what it could not resolve, and classifies a *failed* operation on such
  a plugin as `credential_missing` with the guidance attached.
- **Values are resolved at load, not per call.** A built-in connector resolves
  per operation, so a rotated credential takes effect immediately; a plugin gets
  its config once, at `Init`, and needs
  `cerberus connectors plugin managed load <id>` to see a new one. `managed
  list` reports `missing_secrets` so that state is visible rather than inferred.

**A plugin may not claim a built-in's id.** `DirectoryInstaller.ReservedIDs`
refuses the install, and the daemon fills it from `Registry.BuiltInIDs()` — the
union of instances, factories and definitions, derived so the set shrinks on its
own when `cloudflare` migrates out. WP-0's fallback stays as the safety net for
an inventory registered before the guard existed: restore skips such an entry
with a warning and keeps the registration rather than dropping it.

Only the managed lane reserves ids. `connectors plugin exec` installs into a
throwaway host for one call and registers nothing, so it cannot shadow anything
— and refusing there would break `cerberus connectors write-plugin-prototype
docker`, which exists to demonstrate authoring against a built-in's shape.

Two traps found live, worth remembering for any new operator-facing message:
**`redact.Text` eats its own guidance.** "missing credential token: set
CERBERUS_X_TOKEN" parses as an assignment to a key named `token` and arrives as
"missing credential token: [REDACTED] CERBERUS_X_TOKEN". Do not put an
assignment separator after a word like token/secret/key in a message meant to
instruct. `TestManagerLoadsWithoutRequiredSecret` asserts the message survives
redaction.

The second is the same rule one layer up: **`redact.Marshal` redacts a
names-only field too.** `missing_secrets` matches `SensitiveKey` on the word
SECRET, so the list of names an operator needs came back as `["[REDACTED]"]` —
a field whose entire job is to answer "which one?" refusing to say which one.
`redact.NamesOnlyKey` is the allow-list; add to it only for a field that is
structurally incapable of holding a value, and note it suppresses a key's own
contribution to hiding, never an inherited one.

### Verified live, and what was not

Against the live gateway through the tunnel, with the CLI's one-shot plugin host
(`cerberus connectors plugin exec`, which shares `pluginhost.Manager` with the
daemon-managed lane):

- `get_health` succeeds with no credential configured, and the host logs
  `loaded without required credential token`.
- With `CERBERUS_CONTEXTFORGE_TOKEN` set, that warning disappears — the plugin
  received the value — and `list_gateways` reaches the gateway.
- With none set, `list_gateways` fails `credential_missing` and the guidance
  arrives intact through redaction.

**`list_gateways` returning gateways is still unverified: there is no
ContextForge admin JWT on this machine.** The keychain has no
`contextforge/token` and there is no `~/.cerberus/connector-secrets.yaml`, so
every live call 401s. The channel is proven; the credential is not. Supply a
real JWT by either route and re-run to close this out.

The daemon was not redeployed, so the running daemon still carries the old host.
The managed lane differs from what was exercised only by the wiring line in
`cmd_daemon.go`.

---

## WP-8 — Privilege elevation for `ssh exec`

**Why:** `ssh exec` runs as the operator's own account. Every real deploy in
`~/Projects/tools` needs root for at least one step — `docker load`,
`systemctl`, writing under `/opt/agents` — and `lib/common.sh` has a dedicated
`rsudo` for it. Without elevation, `ssh exec` covers inspection but not the
deploys it was built toward.

**Two things `tools/` already learned the hard way, both load-bearing:**

- **sudo on muctlvaig requires a tty.** `rsudo` uses `ssh -t` for exactly this;
  without it sudo refuses with "sorry, you must have a tty to run sudo". The Go
  client must request a PTY on the session (`session.RequestPty`) when elevating.
- **Arguments must be quoted individually.** `rsudo`'s comment records a real
  bug: `"sudo $*"` joined arguments with spaces and threw the quoting away, so a
  display name of `"GPT-5.6 Terra"` arrived as two arguments, shifted everything
  after it, and SQL failed with `no such column: Terra`. Quote each argument for
  the remote shell (`%q` per argument, as `rsudo` does).

**Design constraints:**

- Elevation is **opt-in per operation**, never implicit. A separate `elevate`
  boolean in the config, not a flag that silently upgrades an existing call.
- An elevated `exec` is destructive and already requires `--ack`. Keep that, and
  say in the dry-run preview that the command runs as root — the preview is what
  an operator reads before acknowledging.
- **No password prompting, ever.** A Cerberus-started ssh has no interactive
  terminal, so a sudo password prompt would hang until timeout with no
  indication why. If sudo needs a password, fail fast and say so. Passwordless
  sudo or nothing — that is an environment fact to report, not to work around.
- Never log the command's output at a level that could carry a secret; elevated
  commands are exactly where credentials appear.

**Acceptance:** an elevated command runs on muctlvaig and reports its own
`id` as root, or reports cleanly that sudo needs a password. An argument
containing a space arrives as one argument — test it, that is the regression
`tools/` paid for.

---

## WP-9 — Recursive directory transfer — DONE 2026-09-17

**Why:** `ssh put`/`get` move one file. A deploy moves a tree — `tools/`
builds a tarball precisely because single-file transfer is not enough.

### Transport decision, probed 2026-09-17

**rsync is not an option for the primary target.**

```
$ cerberus ssh exec muctlvaig --ack -- 'command -v rsync || echo ABSENT'
RSYNC ABSENT
tar: tar (GNU tar) 1.35
```

Locally the machine has `openrsync` (macOS's BSD replacement, protocol 29), not
GNU rsync. So an rsync-based design would depend on a binary that is missing at
one end and old at the other.

It would also break an architectural property worth keeping: **Cerberus shells
out to nothing for SSH.** The connector is entirely in-process over
`golang.org/x/crypto/ssh`. Shelling out to rsync would introduce a second SSH
transport with different auth, different config resolution (`~/.ssh/config`) and
different failure modes from the one the connector already uses.

**Library survey, 2026-09-17 — there is no good one, which is the finding.**
`pkg/sftp`'s `Walk` is the ecosystem's standard answer and recursion is still
yours to write. `bramvdbogaerde/go-scp` (v1.6.1) is tagged but speaks the SCP
protocol, which OpenSSH has deprecated and now implements over SFTP anyway.
`povsister/scp` supports recursion but has no tagged release. `melbahja/goph`
wraps what we already have. So this is a genuine gap in the Go ecosystem, not a
lookup failure — **which makes the sync logic a candidate to extract as an OSS
library** once it has earned its keep here.

Treat `povsister/scp` and the others as **prior art, not a base**: read what they
got right and where they stopped, then build something opinionated. The opinions
worth having are the ones this package already lists — preserve mode, refuse
symlink escapes out of the tree, temp-and-rename per file, honour cancellation
between files as well as within one. Those are the properties a general-purpose
"copy a directory" helper tends not to have, and they are exactly what makes it
safe to point at a real host.

**The sync logic is being built as a standalone library**,
`hollis-labs/go-sftpsync` — see `docs/prompts/build-go-sftpsync.md`. WP-9 becomes
a thin connector binding once it lands: resolve the target, call the library,
map its result onto the operation response. Do not reimplement the walk here.

**It is built on `pkg/sftp`, already a dependency.** It has the primitives: `Walk`,
`ReadDir`, `MkdirAll`, `Chtimes`. Recursion is ours to write, which is a real
cost, but it keeps one transport, one auth path, and no remote dependency.

**The tradeoff, stated honestly:** SFTP has no delta transfer and no
compression. It copies every byte every time. That is fine for what this is for
— compose files, env files, config directories, agent definitions — and wrong
for shipping a 600MB image, which is why `tools/` tars and ships a single blob
for that case. If a large tree shows up, add a tar-over-exec path
(`tar czf - | ssh … tar xzf -`, GNU tar confirmed present on the target) as a
second strategy rather than replacing this one.

**Operations:** `put_dir` and `get_dir`, mirroring `put`/`get`. `put_dir` is
destructive and dry-runnable; the preview should report file count and total
bytes, because that is what an operator needs to decide whether to proceed.

**Constraints:**

- **Preserve mode**, as `put` does — an uploaded script must stay executable.
- **Refuse to follow symlinks out of the tree.** A link to `/etc/shadow` inside
  a synced directory must not exfiltrate it.
- Honour context cancellation between files, not just within one — a cancelled
  transfer over a dead VPN must stop promptly.
- Temp-and-rename per file, matching `put`, so an interrupted sync does not
  leave a half-written file in place of a good one.

**Acceptance:** a directory with nested subdirectories, an executable script and
a symlink round-trips to muctlvaig and back byte-identical, modes intact, with
the symlink not followed outside the tree. Clean up what the test writes.

### What shipped

`hollis-labs/go-sftpsync v0.1.1` carries the walk; Cerberus is the thin binding
the brief called for. `ssh put_dir` and `ssh get_dir`, `cerberus ssh put-dir` /
`get-dir`, `cerberus_ssh_put_dir` / `cerberus_ssh_get_dir`, all five touch
points. Verified live against muctlvaig: the acceptance tree round-tripped
byte-identical with 0755 and 0640 intact, the symlink arrived as a link, and a
link added to `/etc/shadow` was refused with nothing written — then the probe
directory was removed.

Two decisions worth carrying:

- **`get_dir` is not marked destructive**, mirroring `get`. It writes a tree
  rather than one file, which points at the house rule, but it writes only to
  the local machine under a path the operator typed. Marking it destructive
  would also promise a `--dry-run` that cannot be built: previewing a download
  means walking the *remote* tree, and `dryRunPreview` runs before the connector
  is resolved, with no connection to walk it over.
- **`put_dir`'s preview walks the tree a second time**, in
  `sshconn.PreviewDirUpload`, for the same reason — the library's own dry-run
  mode needs an `*sftp.Client` that does not exist yet at preview time. The
  second walk mirrors the library's rules, and
  `TestPreviewDirUploadMatchesLibraryDryRun` runs both against an in-process
  `pkg/sftp` server and fails if the counts diverge. That test is the reason
  this duplication is safe; do not delete it while the duplication stands.

WP-1 would remove the need for both. Once per-connector previews are their own
functions, a preview could take the connector and `get_dir` could gain a real
one — worth revisiting there rather than here.

---

## WP-10 — Prove `list_gateways` against the real gateway

**Small, and it closes the last open question in the ContextForge story.**

`list_gateways`, `list_virtual_servers` and `list_tools` have never run against
a real gateway. Their mapping is unit-tested against a fake, and `get_health`
works live, but the credentialed path is unproven.

Nothing is blocked on code. WP-7 shipped the host secret channel, so this is now
an operator action plus a verification:

1. Obtain a ContextForge JWT. Per `tools/`, the token lives in the gateway
   container's environment on muctlvaig; `cburks` is not in the `docker` group
   there, so it comes from whoever administers that host or from the CF admin UI
   at `http://127.0.0.1:14444/admin/` through the tunnel.
2. Store it: go-keyring service `cerberus`, key `contextforge/token` — or a
   `keychain://` reference under `contextforge` in
   `~/.cerberus/connector-secrets.yaml`.
3. `cerberus connectors plugin managed load contextforge` to pick it up;
   secrets resolve at load, not per call.
4. Run the three read operations and confirm the DTOs carry no credential
   material from a gateway that really has `authToken` set — the test asserts it
   against a fake, and this is the chance to confirm it against the real thing.

**Acceptance:** `list_gateways` returns the real upstream registrations, and
`missing_secrets` is empty in `plugin managed list`. If any response carries a
credential, that is a defect in the DTO allow-list and takes priority over
everything else in this file.


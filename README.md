# Cerberus by Hollis Labs

Cerberus is a single-binary Go control plane for local infrastructure. It
builds, deploys, supervises, and inspects the daemons, dev servers, and
background services the Hollis Labs portfolio runs — over one runtime service
that the CLI, daemon socket, HTTP API, web console, and MCP adapter all share.
It owns execution and derived operational state, not the definitions a
project writes about itself, the data it holds, or the credentials it needs.

> **Pre-release.** Cerberus ships real beta builds — a Homebrew tap, tagged
> GitHub releases, checksummed tarballs — and runs in active internal use
> across Hollis Labs: it's the control plane that deploys and supervises this
> portfolio's own services, including the MCP surface an agent session may be
> calling right now. It has no outside users yet, no compatibility
> guarantees, and no support channel. Built in the open: interfaces and
> behavior can still change without notice.

## What it is today

- **One resource model, four thin surfaces.** CLI, daemon/socket API, web
  console, and MCP adapter all route through the same resource runtime
  service (`internal/cerbapi/resource_runtime_service.go`) — none of them own
  separate execution logic.
- **Local process lifecycle.** `dev_session` for repo-local dev processes,
  `os_service` for launchd-supervised background services, with artifact
  staleness detection so a deploy can't silently ship a rebuild that never
  reinstalled.
- **DNS and domain operations.** Cloudflare zone/DNS management and Namecheap
  domain/nameserver operations, with per-record edits deliberately disabled
  where the provider's API can hide existing records and turn a read-modify-
  write into silent data loss.
- **An MCP server.** `cerberus mcp` (stdio) and `cerberus mcp-http` expose the
  same resource, DNS, and droplet operations as tool calls — this is how
  agents drive infrastructure without a human at a terminal.
- **Early remote execution.** DigitalOcean droplets can already be provisioned
  as Cerberus resources with SSH-based bootstrap, credential delivery, and
  destroy guardrails.

## Where it sits in the stack

```
   agents / operators     Claude Code, Nanite-hosted agents, human at the CLI
         │
    ┌───────────┐
    │ Cerberus  │   build → deploy → supervise → inspect, one runtime service
    └───────────┘
         │
   what it drives         launchd services, dev processes, Docker, DigitalOcean
                           droplets, Cloudflare/Namecheap DNS
```

Cerberus is the ops layer underneath the rest of the portfolio: Nanite,
Torque, Tesseract, Tether and the others run as Cerberus-supervised
resources, but Cerberus doesn't know or care what they do — it only knows how
to build, install, start, stop, and report on them.

## Examples

**Daily driver.** Chrispian ships changes to any portfolio service with
`cerberus resource deploy <id>`, checks for build/install drift with
`resource status`, and manages domain cutovers with `cerberus cloudflare` /
`cerberus domain` — one CLI for every service in the portfolio, local or
remote.

**Agent-driven ops.** This session's `cerberus_resource_*`, `cerberus_dns_*`,
and `cerberus_droplet_*` tools are Cerberus's MCP surface — an agent can
deploy a fix, check whether a service is actually running the code it thinks
it is, or cut a DNS record over, in the same tool-call vocabulary it uses for
everything else.

**Off-laptop execution.** A Claude Code or Codex session can run on a
Cerberus-provisioned DigitalOcean droplet instead of the local machine —
Cerberus handles provisioning, credential delivery over SSH, and teardown;
session lifecycle stays Nanite's concern.

## Roadmap

- **Multi-provider remote infrastructure.** Provisioning via OpenTofu
  (DigitalOcean now, Hetzner next) and configuration via Ansible, covering
  containerized and plain hosts alike, with every operation verifying its own
  effect instead of trusting a 2xx.
- **Retiring the Forge dependency.** Moving deploy orchestration for
  non-containerized apps fully onto the OpenTofu/Ansible path.
- **Remote agent execution, past MVP.** Private networking (Tailscale/
  WireGuard) and snapshot-based hibernate as fast-follows to the current
  destroy-only DigitalOcean lane.

## License

Cerberus is available under the [MIT License](./LICENSE).

## Install

Cerberus is a macOS-first unsigned beta. Pick whichever install path fits your
setup — `cerberus install` (the launchd bootstrap) reads the path of the
running binary, so it works no matter which path you used.

### Option 1: Homebrew

```sh
brew install hollis-labs/tap/cerberus
```

### Option 2: Download a release tarball

```sh
curl -L -o cerberus.tar.gz \
  https://github.com/hollis-labs/cerberus/releases/download/v0.4.0-beta.1/cerberus_0.4.0-beta.1_darwin_arm64.tar.gz
tar -xzf cerberus.tar.gz
install -d "$HOME/.local/bin"
install -m 0755 cerberus "$HOME/.local/bin/"
export PATH="$HOME/.local/bin:$PATH"
```

Released checksums sit next to each tarball as `<archive>.tar.gz.sha256`
plus a combined `checksums.txt` for the whole release.

### Option 3: Build from source

```sh
git clone git@github.com:hollis-labs/cerberus.git
cd cerberus
make build
export PATH="$PWD/bin:$PATH"
```

Or install into a prefix:

```sh
make homebrew-install PREFIX="$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
```

### Option 4: Install with `go install`

```sh
go install github.com/hollis-labs/cerberus/cmd/cerberus@latest
```

### First-time setup

```sh
cerberus init      # write a starter ~/.cerberus/config.yaml
cerberus install   # bootstrap the macOS launch agent (uses the current binary path)
```

See [docs/install.md](docs/install.md) for prerequisites, paths, first-run
walkthrough, and release artifact details. Release packaging steps live in
[docs/release/beta-release-process.md](docs/release/beta-release-process.md).
For Cerberus releasing Cerberus, see
[docs/release/self-release-via-pipeline.md](docs/release/self-release-via-pipeline.md).

## Usage

```bash
cerberus              # show help
cerberus init         # create default config and exit
cerberus install      # bootstrap the macOS launch agent for the daemon
cerberus mcp          # stdio MCP server for local agent clients
cerberus mcp-http     # HTTP MCP endpoint at http://127.0.0.1:4785/mcp by default
cerberus --config /path/to/config.yaml  # use alternate config
```

`cerberus web` and `cerberus mcp-http` are loopback-only. Neither
authenticates its caller yet, so `--listen` must name `localhost` or a literal
loopback IP (`127.0.0.1`, `[::1]`), on any port, and either command refuses to
start otherwise. Other hostnames are refused even when they resolve to
loopback. Both also refuse a request whose `Host` header is not a
loopback name, which defeats DNS rebinding. An SSH local forward
(`ssh -L 9000:127.0.0.1:4785 host`) works; a tunnel or reverse proxy that
forwards a public hostname does not. For browser-based MCP clients,
`mcp-http --allow-origin` adds exact origins to the loopback set.

## Runtime Models

Cerberus now has one local runtime lane:

- `resource` commands: v2 resource workflow backed by `resources:` config entries. Local `process` resources can run as:
  - `dev_session`: repo-local development processes
  - `os_service`: native supervisor-managed background services

Use the `resource` lane for all active local process management across CLI, daemon, socket API, and MCP.

Under the hood, v2 resource operations now route through a shared resource runtime service. CLI, daemon/socket API, and MCP are intended to stay thin wrappers over that one execution layer rather than each owning separate runtime logic.

The daemon health surface reports v2 resource state, and the daemon monitor supervises `dev_session` resources that opt into `auto_restart`.

## CLI Quick Reference

For local v2 `process` resources, the key commands are:

```bash
cerberus resource list
cerberus resource show <resource-id>
cerberus resource status <resource-id>
cerberus resource inspect <resource-id>
cerberus resource doctor <resource-id>
cerberus resource logs <resource-id>
cerberus resource deploy <resource-id>
cerberus resource apply <resource-id>
cerberus resource reload <resource-id>
cerberus resource sync <resource-id>
cerberus resource stop <resource-id>
cerberus resource remove <resource-id>
```

For external infra/domain operations, the current Namecheap and Cloudflare
surfaces include:

```bash
cerberus cloudflare zones
cerberus cloudflare zones create <account-id> <domain> --type full --ack
cerberus cloudflare dns list <zone-id>
cerberus cloudflare dns create <zone-id> --type CNAME --name www --content example.vercel-dns.com --ack
cerberus domain list
cerberus domain status <domain>
cerberus domain nameservers set <domain> <ns1> <ns2> --ack
cerberus dns list <domain>
```

Docker operations run against the daemon's own Docker by default, or against
another Docker host with `--host` (a `DOCKER_HOST` value) or `--context`
(a name from `docker context ls`). The two are mutually exclusive, and the
selection is per command — nothing is left pointing at a remote host
afterwards:

```bash
cerberus docker ps
cerberus docker ps --host ssh://user@host
cerberus docker ps --context azure-dev
cerberus docker logs <container> --host ssh://user@host --lines 100
cerberus docker up <container> --host tcp://10.0.0.4:2376 --ack
cerberus docker down <container> --host ssh://user@host --ack
```

`ssh://` needs key auth to the host and an account that can reach the Docker
socket there; an account outside the remote `docker` group gets an error naming
the host and that recovery.

The ad-hoc `--host`, `--context` and `-f` flags run in your shell, not through
the daemon, and are not available to agents or the web console. A compose file
chooses images, commands and bind mounts, so it is code execution on whichever
daemon runs it. Over the socket, the console and MCP, a docker operation names
a declared resource (below) or a container on the local daemon. A request
carrying `host`, `context` or `compose_file` is refused by name. To let an
agent operate a remote daemon or a stack, declare it as a resource; its
`host`/`context` and `compose_file` then come from the declaration.

`docker up`/`down`/`logs` resolve their argument through the registry, the way
`cerberus ssh` does, so a declared container resource is operated by id — from
the CLI, and from MCP as `resource_id` on `cerberus_docker_up`/`_down`:

```yaml
- id: mtbf-monitor
  type: container
  connector: docker
  config:
    compose_file: /Users/you/Projects/mtbf-monitor/docker-compose.yml
```

```bash
cerberus docker up mtbf-monitor --ack     # no -f needed
cerberus docker down mtbf-monitor --ack   # compose stop: stopped, not removed
cerberus docker logs some-container  # undeclared containers still work
```

`docker down` stops a container or stack (`docker stop`, `docker compose stop`)
and removes nothing. Removal — `docker rm`, or `docker compose down` for a
stack — is the connector's `destroy` operation.

Every connector operation declares an effect class — `read`, `read_sensitive`,
`write`, `lifecycle`, `destructive`, `exec` or `admin` — and every class except
the two reads needs acknowledgment: `--ack` on the CLI, `acknowledged: true`
over MCP and the API. Starting and stopping are `lifecycle`, so `docker up`,
`docker down`, `server start` and `server stop` all take `--ack`. An operation
that writes to the local filesystem needs it whatever its effect, so
`ssh get` and `ssh get-dir` take `--ack` too: a download overwrites the local
path you name.
`cerberus connectors describe <id>` shows each operation's effect.

A `container` or `server` resource is a named handle for connector operations,
not a supervised workload: `resource status` reports it as `unsupervised` and
names the commands that do operate it, and `resource list` shows the same in its
STATUS column. The supervision verbs (`deploy`, `apply`, `reload`, …) do not
apply to it and say so.

SSH runs entirely in-process over `golang.org/x/crypto/ssh` — nothing shells out
to `ssh`, `scp` or `rsync`, so there is one transport, one auth path and no
remote dependency:

```bash
cerberus ssh status <resource-id>
cerberus ssh exec <resource-id> -- 'systemctl status nginx' --ack
cerberus ssh put <resource-id> ./app.env /opt/app/.env --ack
cerberus ssh get <resource-id> /etc/nginx/nginx.conf ./nginx.conf --ack
cerberus ssh put-dir <resource-id> ./deploy /opt/app/deploy --dry-run
cerberus ssh put-dir <resource-id> ./deploy /opt/app/deploy --ack
cerberus ssh get-dir <resource-id> /opt/app/conf ./conf --ack
```

Every `ssh` verb names its target by resource id and nothing else: host, port,
user, key and host-key settings live on the resource. Whoever runs the
operation — the daemon, or the CLI itself when no daemon is running or
`--config` is given — resolves the id against its config. Over the socket, the
web console and MCP, a request that carries `host`, `key_file`,
`allow_insecure_host_key` or any other connection field is refused.

`put-dir`/`get-dir` transfer a tree over SFTP. Permission bits are carried, so
an uploaded script stays executable; each file lands on a temporary name and is
renamed into place, so an interrupted sync leaves the previous file intact; and
a symlink pointing outside the tree is refused rather than followed. `--dry-run`
reports file count and total bytes without connecting.

There is no delta transfer — every byte is copied every time. That fits compose
files, env files, config directories and agent definitions, and is wrong for a
large build tree; tar and ship a single blob for that.

Mental model:

- build source, sync the artifact, and activate it: `cerberus resource deploy <id>`
- start or converge an already-built resource: `cerberus resource apply <id>`
- restart the current installed service without building or syncing: `cerberus resource reload <id>`
- inspect live runtime state: `cerberus resource status <id>`
- inspect full runtime/install details: `cerberus resource inspect <id>`
- diagnose a resource: `cerberus resource doctor <id>`
- tail recent logs: `cerberus resource logs <id>`
- sync artifact only: `cerberus resource sync <id>`
- stop without deleting install state: `cerberus resource stop <id>`
- uninstall runtime state: `cerberus resource remove <id>`

`stop` is the non-destructive pause/stop path. `remove` is destructive for
local `os_service` resources: it unloads the launch agent and removes the
installed artifact tree.

On macOS, `os_service` resources currently use `launchd`. Their runtime artifacts are installed under `~/.cerberus/apps/<project>/<resource>/...` before the launch agent is applied. `resource status` and `resource list` now surface artifact drift plus a recommended next action (`deploy`, `sync`, or `apply`) for artifact-backed services.

`resource list` and `project list` also print a trailing notice on stderr when
registry resolution dropped a registered config or resolved one with warnings:

```
2 config(s) skipped, 1 with warnings
  skipped: torque, tether; warnings: futureapp
  run 'cerberus registry health' for detail
```

A skipped config contributes nothing to the list, so without the notice a short
list is indistinguishable from a complete one. The notice is absent from
`--output json`, whose contract stays a bare array; machine readers can ask the
daemon directly at `/registry/diagnostics`. The console shows the same thing as
a banner on its Resources and Projects pages.

Recommended project pattern:

- `dev`: repo-local iteration paths like Vite, `go run`, and watcher-driven backends should stay on `dev_session` with dev-only ports.
- `uat`: the shared background service you want agents and operators to test against should be `os_service` plus `run_from: artifact`.
- `release`: promoted binaries can use the same `os_service` lane but install from a user-owned or system-owned release location instead of a dev server process.

For artifact-backed services with a declared `build_strategy:` contract, Cerberus now records repo state at sync time and can warn when the installed release/UAT artifact is older than the current Git commit or worktree, even if the workspace binary itself was never rebuilt.

The Cerberus daemon itself now follows this same model as `cerberus-daemon-service`, a v2 local process resource using the canonical launchd label `com.fragments-engine.cerberus`.

For the repo-side rules a project should satisfy before it is added to the v2 lane, see [docs/guides/setting-up-a-project-for-cerberus-v2.md](docs/guides/setting-up-a-project-for-cerberus-v2.md). For recovery help, see [docs/guides/local-runtime-troubleshooting.md](docs/guides/local-runtime-troubleshooting.md).

## Port Map

All ports are unique across the Tiamat suite:

| Port | Service |
|------|---------|
| 1420 | Volon Frontend (Vite) |
| 5173 | Tesseract Frontend (Vite) |
| 5174 | Ion Frontend (Vite) |
| 7765 | Nil Dev |
| 8085 | Volon API |
| 8089 | Tesseract API |
| 8095 | Hadron Daemon |
| 8096 | Ion API |
| 9085 | Volon gRPC (auto, started by Volon API) |
| 34116 | Hadron GUI (Wails desktop) |

## Configuration

Config lives at `~/.cerberus/config.yaml` and must use `version: 2`.

## V2 Resource Example

For the modern local-process path, define a v2 resource:

```yaml
version: 2

projects:
  - id: volon
    name: Volon

resources:
  - id: volon-api
    name: Volon API
    type: process
    project: volon
    connector: local
    config:
      dir: ~/dev/hollis-labs/apps/volon
      command: ["./volon-api", "serve"]
      build_strategy:
        kind: go_standard
        source:
          root: .
        rules:
          output: volon-api
          target: ./cmd/volon-api
      mode: os_service
      supervisor: launchd
      run_from: artifact
```

Notes:

- `mode: dev_session` keeps the process in the repo-local development lane.
- `mode: os_service` uses the native OS supervisor.
- `run_from: artifact` installs a user-area runtime artifact before applying the service.
- For artifact mode, `command[0]` must be a filesystem path, not a bare PATH lookup.
- `~` is expanded by Cerberus for local-process resource paths and env values before launchd sees them.

## V2 Resource Workflow

Typical `os_service` flow on macOS:

```bash
cerberus resource list
cerberus resource status volon-api
cerberus resource deploy volon-api
cerberus resource logs volon-api --stream stderr --lines 100
cerberus resource reload volon-api
cerberus resource stop volon-api
cerberus resource remove volon-api
```

Guidance:

- Use `resource deploy` when your goal is "make the running service match the current source tree".
- Use `resource sync` when the installed artifact is stale and you intentionally do not want to touch the running service yet.
- Use `resource apply` when the correct workspace artifact already exists and the service should be loaded, reloaded, or restarted through `launchd`.
- Use `resource reload` when the installed service definition and artifact are already correct and you only need launchd to restart/kickstart the process.
- Use `resource stop` when you want to stop or pause runtime execution without deleting installed artifact state.
- If status says the repo state changed since the artifact was last synced, prefer `resource deploy` over `apply` or `sync`.
- Use `resource remove` only when you mean "uninstall this runtime instance": unload the launch agent and remove the installed artifact tree.

For registrar and DNS operations:

- `cerberus cloudflare zones create` creates the Cloudflare zone that will own
  DNS for a domain.
- `cerberus domain nameservers set` switches a Namecheap domain to a custom
  nameserver set such as Cloudflare's assigned nameservers.
- `cerberus dns list` inspects the current Namecheap-hosted host records for a
  domain before or after a delegation cutover.
- Namecheap per-record create/delete are disabled, including dry-run. The API
  can hide existing records, making read-modify-write silently destructive.
  Existing CLI/MCP commands return an error without issuing a DNS request.
- The Namecheap connector's `get_dns_record_set` operation includes domain
  `email_type`. Its `set_dns_record_set` operation explicitly replaces all
  hosts and the email mode; it requires acknowledgment, `domain`, `email_type`,
  and a complete `records` array. Use it through connector execution in the
  API, MCP (`cerberus_get_dns_record_set` / `cerberus_set_dns_record_set`)
  or console. It supports dry-run. `FWD` rejects MX/MXE records;
  changing to custom MX is an explicit email-routing change.
  [Namecheap setHosts](https://www.namecheap.com/support/api/methods/domains-dns/set-hosts/)
  deletes omitted records. The API can hide existing records, so its read-back
  is not an authoritative zone backup.

## Cerberus Daemon

Cerberus now has a canonical v2 daemon resource:

```yaml
  - id: cerberus-daemon-service
    name: "Cerberus Daemon Service"
    project: cerberus
    type: process
    connector: local
    config:
      dir: ~/dev/hollis-labs/apps/cerberus
      command: ["./cerberus", "daemon", "--foreground"]
      build_strategy:
        kind: go_standard
        source:
          root: .
        rules:
          output: cerberus
          target: ./cmd/cerberus
      mode: os_service
      supervisor: launchd
      run_from: artifact
      service_name: com.fragments-engine.cerberus
```

For normal lifecycle management, use the resource lane:

```bash
cerberus resource status cerberus-daemon-service
cerberus resource apply cerberus-daemon-service
cerberus resource reload cerberus-daemon-service
cerberus resource remove cerberus-daemon-service
```

`cerberus install` and `cerberus uninstall` remain as bootstrap and recovery helpers for the daemon launch agent when the socket-backed daemon is not available yet.

## Architecture Direction

The accepted direction is:

- `resources:` is the only future local workload model
- local workloads should be `type: process`
- runtime policy should be `mode: dev_session | os_service`
- legacy `services:` are frozen rather than evolved further
- v2 runtime execution lives behind one shared service layer so CLI, API, MCP, and the web console are thin clients

See:

- [docs/adr/0001-local-runtime-backends.md](docs/adr/0001-local-runtime-backends.md)
- [docs/adr/0002-resource-only-local-workload-model.md](docs/adr/0002-resource-only-local-workload-model.md)

## Legacy Status Detection

Uses PID file as primary detection (written on start, validated with signal 0). Falls back to `lsof -ti :<port>` only for services with a port configured. Polls every 2 seconds. Shows PID, uptime, and color-coded status (green=running, red=stopped, yellow=starting/building). If a process crashes on start, the last line of its log is shown as the error.

## Legacy Logs

Service stdout/stderr goes to `$TMPDIR/cerberus-<service-id>.log`.

## Notes

- **Hadron GUI** uses `wails dev` which opens a native desktop window on start — this is inherent to Wails and can't be deferred to click-to-open.
- **Nanite** is also a Wails app and behaves similarly.
- Services with `url` set can be opened in browser; services without (Wails apps) show no `[open]` action.

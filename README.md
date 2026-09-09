# Cerberus by Hollis Labs

Agent-first local infrastructure manager evolving toward a broader control plane. Cerberus now uses the v2 resource model across the CLI, daemon, HTTP/socket API, and MCP surface.

Cerberus is released as permissive open source under the MIT license. The
public product identity is **Cerberus by Hollis Labs**. The repo, CLI, binary,
and day-to-day docs primarily refer to it simply as **Cerberus**.

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
go install github.com/chrispian/cerberus/cmd/cerberus@latest
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

## License

Cerberus is available under the [MIT License](./LICENSE).

## Usage

```bash
cerberus              # show help
cerberus init         # create default config and exit
cerberus install      # bootstrap the macOS launch agent for the daemon
cerberus mcp          # stdio MCP server for local agent clients
cerberus mcp-http     # HTTP MCP endpoint at http://127.0.0.1:4785/mcp by default
cerberus --config /path/to/config.yaml  # use alternate config
```

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

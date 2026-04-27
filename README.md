# Cerberus

Agent-first local infrastructure manager evolving toward a broader control plane. Cerberus still supports the original TUI service-manager workflow, but the modern path is the v2 resource model used by the CLI, daemon, HTTP/socket API, and MCP surface.

## Install

```bash
go install ./cmd/cerberus/
```

Binary goes to `~/go/bin/cerberus`. On first run, creates `~/.cerberus/config.yaml` with default service definitions.

## Usage

```bash
cerberus              # launch TUI
cerberus --init       # create default config and exit
cerberus --config /path/to/config.yaml  # use alternate config
```

## Runtime Models

Cerberus currently has two local runtime lanes:

- `service` commands: legacy v1 service-manager workflow backed by `services:` config entries, PID files, and the daemon monitor.
- `resource` commands: v2 resource workflow backed by `resources:` config entries. Local `process` resources can run as:
  - `dev_session`: repo-local development processes
  - `os_service`: native supervisor-managed background services

Use the `service` lane when you are operating old-style `services:` entries or the TUI. Use the `resource` lane for new v2 local process management across CLI, daemon, socket API, and MCP.

For local v2 `process` resources, the key commands are:

```bash
cerberus resource list
cerberus resource show <resource-id>
cerberus resource status <resource-id>
cerberus resource apply <resource-id>
cerberus resource sync <resource-id>
cerberus resource remove <resource-id>
```

On macOS, `os_service` resources currently use `launchd`. Their runtime artifacts are installed under `~/.cerberus/apps/<project>/<resource>/...` before the launch agent is applied. `resource status` and `resource list` now surface artifact drift plus a recommended next action (`sync` or `apply`) for artifact-backed services.

## Controls

| Key | Action |
|-----|--------|
| `j`/`k` or arrows | Navigate |
| `s` | Start selected service |
| `x` | Stop selected (SIGTERM, SIGKILL after 5s) |
| `r` | Restart |
| `b` | Build (run configured build command) |
| `enter` / `l` | Open URL in browser |
| `a` | Start all |
| `X` | Stop all |
| `tab` | Cycle sort (name / status / port) |
| `/` | Filter by name or tag |
| `q` | Quit |

## Port Map

All ports are unique across the Tiamat suite:

| Port | Service |
|------|---------|
| 1420 | Volon Frontend (Vite) |
| 5173 | Vanta Conduit Frontend (Vite) |
| 5174 | Carrier Frontend (Vite) |
| 7765 | Nanite API (embedded) |
| 8080 | Vanta Conduit API |
| 8085 | Volon API |
| 8095 | Hadron Daemon |
| 8096 | Carrier API |
| 9085 | Volon gRPC (auto, started by Volon API) |
| 34116 | Hadron GUI (Wails desktop) |

## Configuration

Config lives at `~/.cerberus/config.yaml`. Each service entry:

```yaml
services:
  - id: conduit-api
    name: "Vanta Conduit API"
    project: conduit
    dir: ~/Projects-apps/conduit
    command: ["./contextd", "serve", "--addr", ":8080"]
    build: ["go", "build", "-o", "contextd", "./cmd/contextd/"]  # optional build command
    env:                          # optional env vars (~ expanded)
      CONTEXTD_ROOT: ~/.conduit
    env_file: .env                # optional dotenv file (relative to dir)
    url: http://127.0.0.1:8080   # opened by enter/l key
    port: 8080                    # used for status polling (lsof)
    health: http://127.0.0.1:8080/v1/health/readiness
    tags: [api, daemon, go]       # filterable with /
    protected: true               # blocks external stop/restart via MCP
    auto_restart: true            # daemon monitor restarts if crashed
```

**Never set `port: 0`** — omit the field entirely for services without a port. `lsof -ti :0` returns random system PIDs, causing false "running" status.

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
      dir: ~/Projects-apps/volon
      command: ["./volon-api", "serve"]
      build: ["go", "build", "-o", "volon-api", "./cmd/volon-api"]
      mode: os_service
      supervisor: launchd
      run_from: artifact
```

Notes:

- `mode: dev_session` keeps the process in the repo-local development lane.
- `mode: os_service` uses the native OS supervisor.
- `run_from: artifact` installs a user-area runtime artifact before applying the service.
- For artifact mode, `command[0]` must be a filesystem path, not a bare PATH lookup.

## V2 Resource Workflow

Typical `os_service` flow on macOS:

```bash
cerberus resource list
cerberus resource status volon-api
cerberus resource sync volon-api
cerberus resource apply volon-api
cerberus resource remove volon-api
```

Guidance:

- Use `resource sync` when the installed artifact is stale and the service is stopped.
- Use `resource apply` when the service should be loaded, reloaded, or restarted through `launchd`.
- Use `resource remove` to unload the launch agent and remove the installed artifact tree.

## Status Detection

Uses PID file as primary detection (written on start, validated with signal 0). Falls back to `lsof -ti :<port>` only for services with a port configured. Polls every 2 seconds. Shows PID, uptime, and color-coded status (green=running, red=stopped, yellow=starting/building). If a process crashes on start, the last line of its log is shown as the error.

## Logs

Service stdout/stderr goes to `$TMPDIR/cerberus-<service-id>.log`.

## Notes

- **Hadron GUI** uses `wails dev` which opens a native desktop window on start — this is inherent to Wails and can't be deferred to click-to-open.
- **Nanite** is also a Wails app and behaves similarly.
- Services with `url` set can be opened in browser; services without (Wails apps) show no `[open]` action.

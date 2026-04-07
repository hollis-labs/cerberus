# Cerberus

TUI service manager for the Project Tiamat family. Start, stop, and monitor all project daemons and dev servers from one screen.

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

## Status Detection

Uses PID file as primary detection (written on start, validated with signal 0). Falls back to `lsof -ti :<port>` only for services with a port configured. Polls every 2 seconds. Shows PID, uptime, and color-coded status (green=running, red=stopped, yellow=starting/building). If a process crashes on start, the last line of its log is shown as the error.

## Logs

Service stdout/stderr goes to `$TMPDIR/cerberus-<service-id>.log`.

## Notes

- **Hadron GUI** uses `wails dev` which opens a native desktop window on start — this is inherent to Wails and can't be deferred to click-to-open.
- **Nanite** is also a Wails app and behaves similarly.
- Services with `url` set can be opened in browser; services without (Wails apps) show no `[open]` action.

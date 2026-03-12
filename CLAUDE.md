# Cerberus — agent boot

## Agent Auto-Boot Override

This repo boots as `worker`. **Skip profile selection** — go directly to:

4. Read `.agentrc/agent-boot.md`
5. Read `.agentrc/boot/worker.md`
6. Read `.agentrc/bootstrap.md`
7. Follow the worker profile instructions — emit boot confirmation and begin work

## Project Overview

Cerberus is a TUI service manager for the Tiamat ecosystem. It provides a Bubble Tea interface to start, stop, and monitor all project daemons and dev servers from one screen.

## Build & Test

```bash
go build ./cmd/cerberus/
go test ./...
```

## Architecture

- `cmd/cerberus/` — Entry point, TUI application, daemon mode
- `internal/config/` — Configuration loading and service definitions
- `internal/service/` — Service lifecycle management (start/stop/monitor)
- `internal/daemon/` — Health monitor, auto-restart, alerting
- `internal/mcp/` — MCP tools (status, health, lifecycle)
- `internal/tui/` — Bubble Tea TUI models and views

## CRITICAL: Never Set port: 0

**Do NOT set `port: 0` on any service in the config.** Omit the `port` field entirely for services that don't listen on a TCP port (CLIs, daemons without HTTP, build-only entries).

**Why:** Cerberus uses `lsof -ti :<port>` for status detection. `lsof -ti :0` returns arbitrary macOS system PIDs (identityservicesd, mDNSResponder, etc.), causing false-positive "running" status in the TUI and daemon monitor. This has caused repeated incidents.

**Correct:**
```yaml
- id: my-daemon
  command: ["./my-daemon"]
  # No port field — detection uses PID file only
  tags: [daemon]
```

**Wrong:**
```yaml
- id: my-daemon
  command: ["./my-daemon"]
  port: 0        # NEVER do this
  tags: [daemon]
```

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

- `cmd/cerberus/` — Entry point, TUI application
- `internal/config/` — Configuration loading and service definitions
- `internal/service/` — Service lifecycle management (start/stop/monitor)
- `internal/tui/` — Bubble Tea TUI models and views
